// Package authn is authentication server for AIStore.
/*
 * Copyright (c) 2018-2023, NVIDIA CORPORATION. All rights reserved.
 */
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/NVIDIA/aistore/api/env"
	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/cmn/cos"
	"github.com/NVIDIA/aistore/cmn/fname"
	"github.com/NVIDIA/aistore/cmn/jsp"
	"github.com/NVIDIA/aistore/cmn/kvdb"
	"github.com/NVIDIA/aistore/cmn/nlog"
)

var (
	build     string
	buildtime string

	configPath string
)

func init() {
	flag.StringVar(&configPath, "config", "", svcName+" configuration")
}

func logFlush() {
	for {
		time.Sleep(time.Minute) // TODO: must be configurable
		nlog.Flush(nlog.ActNone)
	}
}

func main() {
	var configDir string
	if len(os.Args) == 2 && os.Args[1] == "version" {
		printVer()
		os.Exit(0)
	}
	if len(os.Args) == 1 || (len(os.Args) == 2 && strings.Contains(os.Args[1], "help")) {
		printVer()
		flag.PrintDefaults()
		os.Exit(0)
	}
	installSignalHandler()
	flag.Parse()

	confDirFlag := flag.Lookup("config")
	if confDirFlag != nil {
		configDir = confDirFlag.Value.String()
	}
	if configDir == "" {
		configDir = os.Getenv(env.AuthN.ConfDir)
	}
	if configDir == "" {
		cos.ExitLogf("Missing %s configuration file (to specify, use '-%s' option or '%s' environment)",
			svcName, confDirFlag.Name, env.AuthN.ConfDir)
	}
	configPath = filepath.Join(configDir, fname.AuthNConfig)
	if _, err := jsp.LoadMeta(configPath, Conf); err != nil {
		cos.ExitLogf("Failed to load configuration from %q: %v", configPath, err)
	}
	Conf.Init()
	if val := os.Getenv(env.AuthN.SecretKey); val != "" {
		Conf.SetSecret(&val)
	}
	if err := updateLogOptions(); err != nil {
		cos.ExitLogf("Failed to set up logger: %v", err)
	}
	if Conf.Verbose() {
		nlog.Infof("Loaded configuration from %s", configPath)
	}

	dbPath := filepath.Join(configDir, fname.AuthNDB)
	driver, err := kvdb.NewBuntDB(dbPath)
	if err != nil {
		cos.ExitLogf("Failed to init local database: %v", err)
	}
	mgr, err := newMgr(driver)
	if err != nil {
		cos.ExitLogf("Failed to init manager: %v", err)
	}

	nlog.Infof("Version %s (build %s)\n", cmn.VersionAuthN+"."+build, buildtime)

	go logFlush()

	srv := newServer(mgr)
	err = srv.Run()

	nlog.Flush(nlog.ActExit)
	cos.Close(mgr.db)
	if err != nil {
		cos.ExitLogf("Server failed: %v", err)
	}
}

func updateLogOptions() error {
	logDir := cos.GetEnvOrDefault(env.AuthN.LogDir, Conf.Log.Dir)
	if err := cos.CreateDir(logDir); err != nil {
		return fmt.Errorf("failed to create log dir %q, err: %v", logDir, err)
	}
	nlog.SetPre(logDir, "auth")
	return nil
}

func installSignalHandler() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-c
		os.Exit(0)
	}()
}

func printVer() {
	fmt.Printf("version %s (build %s)\n", cmn.VersionAuthN+"."+build, buildtime)
}
