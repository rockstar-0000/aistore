// Package xmeta provides low-level tools to format or extract
// into plain text some of the AIS control structures.
/*
 * Copyright (c) 2018-2022, NVIDIA CORPORATION. All rights reserved.
 */
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/NVIDIA/aistore/cluster"
	"github.com/NVIDIA/aistore/cluster/meta"
	"github.com/NVIDIA/aistore/cluster/mock"
	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/cmn/cos"
	"github.com/NVIDIA/aistore/cmn/jsp"
	"github.com/NVIDIA/aistore/ec"
	"github.com/NVIDIA/aistore/fs"
	"github.com/NVIDIA/aistore/volume"
	jsoniter "github.com/json-iterator/go"
)

// TODO: can LOM be used? LOM.Copies outside of a target has a lot of empty fields.
type lomInfo struct {
	Attrs  *cmn.ObjAttrs `json:"attrs"`
	Copies []string      `json:"copies,omitempty"`
}

var flags struct {
	in, out string
	format  string
	extract bool
	help    bool
}

const (
	helpMsg = `Build:
	go install xmeta.go

Examples:
	xmeta -h                                          - show usage
	# Smap:
	xmeta -x -in=~/.ais0/.ais.smap                    - extract Smap to STDOUT
	xmeta -x -in=~/.ais0/.ais.smap -out=/tmp/smap.txt - extract Smap to /tmp/smap.txt
	xmeta -x -in=./.ais.smap -f smap                  - extract Smap to STDOUT with explicit source format
	xmeta -in=/tmp/smap.txt -out=/tmp/.ais.smap       - format plain-text /tmp/smap.txt
	# BMD:
	xmeta -x -in=~/.ais0/.ais.bmd                     - extract BMD to STDOUT
	xmeta -x -in=~/.ais0/.ais.bmd -out=/tmp/bmd.txt   - extract BMD to /tmp/bmd.txt
	xmeta -x -in=./.ais.bmd -f bmd                    - extract BMD to STDOUT with explicit source format
	xmeta -in=/tmp/bmd.txt -out=/tmp/.ais.bmd         - format plain-text /tmp/bmd.txt
	# RMD:
	xmeta -x -in=~/.ais0/.ais.rmd                     - extract RMD to STDOUT
	xmeta -x -in=~/.ais0/.ais.rmd -out=/tmp/rmd.txt   - extract RMD to /tmp/rmd.txt
	xmeta -x -in=./.ais.rmd -f rmd                    - extract RMD to STDOUT with explicit source format
	xmeta -in=/tmp/rmd.txt -out=/tmp/.ais.rmd         - format plain-text /tmp/rmd.txt
	# Config:
	xmeta -x -in=~/.ais0/.ais.conf                    - extract Config to STDOUT
	xmeta -x -in=~/.ais0/.ais.conf -out=/tmp/conf.txt - extract Config to /tmp/config.txt
	xmeta -x -in=./.ais.conf -f conf                  - extract Config to STDOUT with explicit source format
	xmeta -in=/tmp/conf.txt -out=/tmp/.ais.conf       - format plain-text /tmp/config.txt
	# VMD:
	xmeta -x -in=~/.ais0/.ais.vmd                     - extract VMD to STDOUT
	xmeta -x -in=~/.ais0/.ais.vmd -out=/tmp/vmd.txt   - extract VMD to /tmp/vmd.txt
	xmeta -x -in=./.ais.vmd -f conf                   - extract VMD to STDOUT with explicit source format
	xmeta -in=/tmp/vmd.txt -out=/tmp/.ais.vmd         - format plain-text /tmp/vmd.txt
	# EC Metadata:
	xmeta -x -in=/data/@ais/abc/%mt/readme            - extract Metadata to STDOUT with auto-detection (by directory name)
	xmeta -x -in=./readme -f mt                       - extract Metadata to STDOUT with explicit source format
	# LOM (readonly, no format auto-detection):
	xmeta -x -in=/data/@ais/abc/%ob/img001.tar -f lom                   - extract LOM to STDOUT
	xmeta -x -in=/data/@ais/abc/%ob/img001.tar -out=/tmp/lom.txt -f lom - extract LOM to /tmp/lom.txt
`
)

var m = map[string]struct {
	extract func() error
	format  func() error
	what    string
}{
	"smap": {extractSmap, formatSmap, "Smap"},
	"bmd":  {extractBMD, formatBMD, "BMD"},
	"rmd":  {extractRMD, formatRMD, "RMD"},
	"conf": {extractConfig, formatConfig, "Config"},
	"vmd":  {extractVMD, formatVMD, "VMD"},
	"mt":   {extractECMeta, formatECMeta, "EC Metadata"},
	"lom":  {extractLOM, formatLOM, "LOM"},
}

// "extract*" routines expect AIS-formatted (smap, bmd, rmd, etc.)

func extractSmap() error   { return extractMeta(&meta.Smap{}) }
func extractBMD() error    { return extractMeta(&meta.BMD{}) }
func extractRMD() error    { return extractMeta(&meta.RMD{}) }
func extractConfig() error { return extractMeta(&cmn.ClusterConfig{}) }
func extractVMD() error    { return extractMeta(&volume.VMD{}) }

// "format*" routines require output filename

func formatSmap() error   { return formatMeta(&meta.Smap{}) }
func formatBMD() error    { return formatMeta(&meta.BMD{}) }
func formatRMD() error    { return formatMeta(&meta.RMD{}) }
func formatConfig() error { return formatMeta(&cmn.ClusterConfig{}) }
func formatVMD() error    { return formatMeta(&volume.VMD{}) }
func formatLOM() error    { return errors.New("saving LOM is unsupported") }

func main() {
	newFlag := flag.NewFlagSet(os.Args[0], flag.ExitOnError) // discard flags of imported packages
	newFlag.BoolVar(&flags.extract, "x", false,
		"true: extract AIS-formatted metadata type, false: pack and AIS-format plain-text metadata")
	newFlag.StringVar(&flags.in, "in", "", "fully-qualified input filename")
	newFlag.StringVar(&flags.out, "out", "", "output filename (optional when extracting)")
	newFlag.BoolVar(&flags.help, "h", false, "print usage and exit")
	newFlag.StringVar(&flags.format, "f", "", "override automatic format detection (one of smap, bmd, rmd, conf, vmd, mt, lom)")
	newFlag.Parse(os.Args[1:])
	if flags.help || len(os.Args[1:]) == 0 {
		newFlag.Usage()
		hmsg := helpMsg
		fmt.Print(hmsg)
		os.Exit(0)
	}

	flags.in = cos.ExpandPath(flags.in)
	if flags.out != "" {
		flags.out = cos.ExpandPath(flags.out)
	}
	in := strings.ToLower(flags.in)
	f, what := detectFormat(in)
	if err := f(); err != nil {
		if flags.extract {
			fmt.Printf("Failed to extract %s from %s: %v\n", what, in, err)
		} else {
			fmt.Printf("Cannot format %s: plain-text input %s, error=\"%v\"\n", what, in, err)
		}
	}
}

func detectFormat(in string) (f func() error, what string) {
	if flags.format == "" {
		return parse(in, flags.extract)
	}
	e, ok := m[flags.format]
	if !ok {
		fmt.Printf("Invalid file format %q. Supported formats are (", flags.format)
		for k := range m {
			fmt.Printf("%s, ", k)
		}
		fmt.Printf(")\n")
		os.Exit(1)
	}
	f, what = e.format, e.what
	if flags.extract {
		f = e.extract
	}
	return
}

func parse(in string, extract bool) (f func() error, what string) {
	var all []string
	for k, e := range m {
		if !strings.Contains(in, k) {
			all = append(all, e.what)
			continue
		}
		if extract {
			f, what = e.extract, e.what
		} else {
			f, what = e.format, e.what
		}
		break
	}
	if what == "" {
		fmt.Printf("Failed to parse %q for AIS metadata type, one of: %q\n", in, all)
		os.Exit(1)
	}
	return
}

func extractMeta(v jsp.Opts) (err error) {
	f := os.Stdout
	if flags.out != "" {
		f, err = cos.CreateFile(flags.out)
		if err != nil {
			return
		}
	}
	_, err = jsp.LoadMeta(flags.in, v)
	if err != nil {
		return
	}
	s, _ := jsoniter.MarshalIndent(v, "", " ")
	_, err = fmt.Fprintln(f, string(s))
	return err
}

func extractECMeta() (err error) {
	f := os.Stdout
	if flags.out != "" {
		f, err = cos.CreateFile(flags.out)
		if err != nil {
			return
		}
	}
	var v *ec.Metadata
	v, err = ec.LoadMetadata(flags.in)
	if err != nil {
		return
	}
	s, _ := jsoniter.MarshalIndent(v, "", " ")
	_, err = fmt.Fprintln(f, string(s))
	return err
}

func formatMeta(v jsp.Opts) error {
	if flags.out == "" {
		return errors.New("output filename (the -out option) must be defined")
	}
	if _, err := jsp.Load(flags.in, v, jsp.Plain()); err != nil {
		return err
	}
	return jsp.SaveMeta(flags.out, v, nil)
}

func formatECMeta() error {
	if flags.out == "" {
		return errors.New("output filename (the -out option) must be defined")
	}
	v := &ec.Metadata{}
	if _, err := jsp.Load(flags.in, v, jsp.Plain()); err != nil {
		return err
	}
	file, err := cos.CreateFile(flags.out)
	if err != nil {
		return err
	}
	defer cos.Close(file)
	buf := v.NewPack()
	_, err = file.Write(buf)
	if err != nil {
		cos.RemoveFile(flags.out)
	}
	return err
}

func extractLOM() (err error) {
	f := os.Stdout
	if flags.out != "" {
		f, err = cos.CreateFile(flags.out)
		if err != nil {
			return
		}
	}
	os.Setenv(cluster.DumpLomEnvVar, "1")
	fs.TestNew(nil)
	bmdMock := mock.NewBaseBownerMock()
	t := &mock.TargetMock{BO: bmdMock}
	cluster.Init(t)
	lom := &cluster.LOM{FQN: flags.in}
	err = lom.LoadMetaFromFS()
	if err != nil {
		return
	}
	lmi := lomInfo{Attrs: lom.ObjAttrs()}
	if lom.HasCopies() {
		lmi.Copies = make([]string, 0, lom.NumCopies())
		for mp := range lom.GetCopies() {
			lmi.Copies = append(lmi.Copies, mp)
		}
	}
	s, _ := jsoniter.MarshalIndent(lmi, "", " ")
	_, err = fmt.Fprintln(f, string(s))
	return err
}
