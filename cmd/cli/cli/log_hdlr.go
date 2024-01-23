// Package cli provides easy-to-use commands to manage, monitor, and utilize AIS clusters.
// This file handles commands that interact with the cluster.
/*
 * Copyright (c) 2021-2024, NVIDIA CORPORATION. All rights reserved.
 */
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/NVIDIA/aistore/api"
	"github.com/NVIDIA/aistore/api/apc"
	"github.com/NVIDIA/aistore/cmn/archive"
	"github.com/NVIDIA/aistore/cmn/cos"
	"github.com/NVIDIA/aistore/core/meta"
	"github.com/NVIDIA/aistore/sys"
	"github.com/urfave/cli"
)

const clusterCompletion = "cluster"

var (
	nodeLogFlags = map[string][]cli.Flag{
		commandShow: append(
			longRunFlags,
			logSevFlag,
			logFlushFlag,
		),
		commandGet: append(
			longRunFlags,
			logSevFlag,
			yesFlag,
			allLogsFlag,
		),
	}

	// 'show log' and 'log show'
	showCmdLog = cli.Command{
		Name: cmdLog,
		Usage: fmt.Sprintf("for a given node: show its current log (use %s to update, %s for details)",
			qflprn(refreshFlag), qflprn(cli.HelpFlag)),
		ArgsUsage:    showLogArgument,
		Flags:        nodeLogFlags[commandShow],
		Action:       showNodeLogHandler,
		BashComplete: suggestAllNodes,
	}
	getCmdLog = cli.Command{
		Name: commandGet,
		Usage: "download the current log or entire log history from a selected node or all nodes, e.g.:\n" +
			indent4 + "\t - 'ais log get NODE_ID /tmp' - download the specified node's current log; save the result to the specified directory;\n" +
			indent4 + "\t - 'ais log get NODE_ID /tmp/out --refresh 10' - download the current log as /tmp/out\n" +
			indent4 + "\t    keep updating (ie., appending) the latter every 10s;\n" +
			indent4 + "\t - 'ais log get cluster /tmp' - download TAR.GZ archived logs from _all_ nodes in the cluster\n" +
			indent4 + "\t    (note that 'cluster' implies '--all'), and save the result to the specified destination;\n" +
			indent4 + "\t - 'ais log get NODE_ID --all' - download the node's TAR.GZ log archive\n" +
			indent4 + "\t - 'ais log get NODE_ID --all --severity e' - TAR.GZ archive of (only) logged errors and warnings",
		ArgsUsage: getLogArgument,
		Flags:     nodeLogFlags[commandGet],
		Action:    getLogHandler,
		BashComplete: func(c *cli.Context) {
			fmt.Println(clusterCompletion)
			suggestAllNodes(c)
		},
	}

	// top-level
	logCmd = cli.Command{
		Name:  commandLog,
		Usage: "view ais node's log in real time; download the current log; download all logs (history)",
		Subcommands: []cli.Command{
			makeAlias(showCmdLog, "", true, commandShow),
			getCmdLog,
		},
	}
)

func showNodeLogHandler(c *cli.Context) error {
	return _currentLog(c)
}

func getLogHandler(c *cli.Context) error {
	if c.NArg() < 1 {
		return missingArgumentsError(c, c.Command.ArgsUsage)
	}
	//
	// either (1) show (or get) the current log
	//
	all := flagIsSet(c, allLogsFlag) || c.Args().Get(0) == clusterCompletion
	if !all {
		return _currentLog(c)
	}

	//
	// or (2) all archived logs from a) single node or b) entire cluster
	//
	sev, err := parseLogSev(c)
	if err != nil {
		return err
	}
	if flagIsSet(c, refreshFlag) {
		return incorrectUsageMsg(c, errFmtExclusive, qflprn(allLogsFlag), qflprn(refreshFlag))
	}
	outFile := c.Args().Get(1)

	if c.Args().Get(0) == clusterCompletion {
		// b)
		if flagIsSet(c, refreshFlag) {
			return fmt.Errorf("flag %s requires selecting a node (to view or download its current log), see %s for details",
				qflprn(refreshFlag), qflprn(cli.HelpFlag))
		}
		err = _getAllClusterLogs(c, sev, outFile)
	} else {
		// a)
		node, sname, errV := getNode(c, c.Args().Get(0))
		if errV != nil {
			if isErrDoesNotExist(errV) {
				var hint string
				// not a node but maybe OUT_DIR
				if all {
					finfo, errEx := os.Stat(c.Args().Get(0))
					if errEx == nil && finfo.IsDir() {
						err = _getAllClusterLogs(c, sev, outFile)
						goto ret
					}
				}

				// with a hint
				hint = "Hint:  "
				errV = fmt.Errorf("%v\n"+hint+"did you mean 'ais log get %s %s'?", errV, clusterCompletion, c.Args().Get(0))
			}
			return errV
		}
		err = _getAllNodeLogs(c, node, sev, outFile, sname)
	}
ret:
	if err == nil {
		actionDone(c, "Done")
	}
	return err
}

func _getAllClusterLogs(c *cli.Context, sev, outFile string) error {
	smap, err := getClusterMap(c)
	if err != nil {
		return err
	}
	if outFile == fileStdIO {
		return errors.New("cannot download archived logs to standard output")
	}

	wg := cos.NewLimitedWaitGroup(sys.NumCPU(), smap.Count())
	_alll(c, smap.Pmap, sev, outFile, wg)
	_alll(c, smap.Tmap, sev, outFile, wg)
	wg.Wait()
	return nil
}

func _alll(c *cli.Context, nodeMap meta.NodeMap, sev, outFile string, wg cos.WG) {
	for _, si := range nodeMap {
		wg.Add(1)
		go func(si *meta.Snode) {
			sname := si.StringEx()
			if err := _getAllNodeLogs(c, si, sev, outFile, sname); err != nil {
				actionWarn(c, sname+" returned error: "+err.Error())
			}
			wg.Done()
		}(si)
	}
}

func _getAllNodeLogs(c *cli.Context, node *meta.Snode, sev, outFile, sname string) error {
	var (
		tempdir, fname, s string
		confirmed         bool
	)
	if outFile == fileStdIO {
		return errors.New("cannot download archived logs to standard output")
	}
	if outFile == "" {
		tempdir = filepath.Join(os.TempDir(), "aislogs")
		if err := cos.CreateDir(tempdir); err != nil {
			return fmt.Errorf("failed to create temp dir %s: %v", tempdir, err)
		}
		fname = apc.Target + "-" + node.ID() + archive.ExtTarGz
		if node.IsProxy() {
			fname = apc.Proxy + "-" + node.ID() + archive.ExtTarGz
		}
		outFile = filepath.Join(tempdir, fname)
	} else {
		outFile, confirmed = _logDestName(c, node, outFile)
		if !confirmed {
			return nil
		}
		if outFile != discardIO {
			if !strings.HasSuffix(outFile, archive.ExtTarGz) && !strings.HasSuffix(outFile, archive.ExtTgz) {
				outFile += archive.ExtTarGz
			}
		}
	}
	file, err := os.Create(outFile)
	if err != nil {
		return fmt.Errorf("failed to create destination %s: %v", outFile, err)
	}

	if sev == apc.LogErr || sev == apc.LogWarn {
		s = " (errors and warnings)"
	}
	if outFile != discardIO {
		fmt.Fprintf(c.App.Writer, "Downloading %s%s logs as %s\n", sname, s, outFile)
	} else {
		fmt.Fprintf(c.App.Writer, "Downloading (and discarding) %s%s logs\n", sname, s)
	}

	// call api
	args := api.GetLogInput{Writer: file, Severity: sev, All: true}
	_, err = api.GetDaemonLog(apiBP, node, args)
	file.Close()
	return V(err)
}

// common (show, get) one log
func _currentLog(c *cli.Context) error {
	if c.NArg() < 1 {
		return missingArgumentsError(c, c.Command.ArgsUsage)
	}
	node, sname, err := getNode(c, c.Args().Get(0))
	if err != nil {
		if isErrDoesNotExist(err) {
			// with a hint
			err = fmt.Errorf("%v\n(Hint: did you mean 'ais log get %s %s'?)", err, clusterCompletion, c.Args().Get(0))
		}
		return err
	}
	// destination
	outFile := c.Args().Get(1)

	sev, err := parseLogSev(c)
	if err != nil {
		return err
	}

	firstIteration := setLongRunParams(c, 0)
	if firstIteration && flagIsSet(c, logFlushFlag) {
		var (
			flushRate = parseDurationFlag(c, logFlushFlag)
			nvs       = make(cos.StrKVs)
		)
		config, err := api.GetDaemonConfig(apiBP, node)
		if err != nil {
			return V(err)
		}
		if config.Log.FlushTime.D() != flushRate {
			nvs[nodeLogFlushName] = flushRate.String()
			if err := api.SetDaemonConfig(apiBP, node.ID(), nvs, true /*transient*/); err != nil {
				return V(err)
			}
			warn := fmt.Sprintf("run 'ais config node %s inherited %s %s' to change it back",
				sname, nodeLogFlushName, config.Log.FlushTime)
			actionWarn(c, warn)
			briefPause(2)
			fmt.Fprintln(c.App.Writer)
		}
	}

	var (
		file     *os.File
		readsize int64
		s        string
		writer   = os.Stdout // default
		args     = api.GetLogInput{Severity: sev, Offset: getLongRunOffset(c)}
	)
	if outFile != fileStdIO && outFile != "" /* empty => standard output */ {
		var confirmed bool
		outFile, confirmed = _logDestName(c, node, outFile)
		if !confirmed {
			return nil
		}
		if args.Offset == 0 {
			if file, err = os.Create(outFile); err != nil {
				return err
			}
			setLongRunOutfile(c, file)
			if sev == apc.LogErr || sev == apc.LogWarn {
				s = " (errors and warnings)"
			}
			if outFile != discardIO {
				fmt.Fprintf(c.App.Writer, "Downloading %s%s log as %s ...\n", sname, s, outFile)
			} else {
				fmt.Fprintf(c.App.Writer, "Downloading (and discarding) %s%s log ...\n", sname, s)
			}
		} else {
			file = getLongRunOutfile(c)
		}
		writer = file
	}

	// call api
	args.Writer = writer
	readsize, err = api.GetDaemonLog(apiBP, node, args)
	if err == nil {
		if isLongRun(c) {
			addLongRunOffset(c, readsize)
		} else if file != nil {
			file.Close()
			actionDone(c, "Done")
		}
	} else if file != nil {
		if off, _ := file.Seek(0, io.SeekCurrent); off == 0 {
			file.Close()
			os.Remove(outFile)
			setLongRunOutfile(c, nil)
			file = nil
		}
		if file != nil && !isLongRun(c) {
			file.Close()
		}
	}
	return V(err)
}

func parseLogSev(c *cli.Context) (sev string, err error) {
	sev = strings.ToLower(parseStrFlag(c, logSevFlag))
	if sev != "" {
		switch sev[0] {
		case apc.LogInfo[0]:
			sev = apc.LogInfo
		case apc.LogWarn[0]:
			sev = apc.LogWarn
		case apc.LogErr[0]:
			sev = apc.LogErr
		default:
			err = fmt.Errorf("invalid log severity, expecting empty string or one of: %s, %s, %s",
				apc.LogInfo, apc.LogWarn, apc.LogErr)
		}
	}
	return
}

func _logDestName(c *cli.Context, node *meta.Snode, outFile string) (string, bool) {
	if outFile == discardIO {
		return outFile, true
	}
	finfo, errEx := os.Stat(outFile)
	if errEx != nil {
		return outFile, true
	}
	// destination: directory | file (confirm overwrite)
	if finfo.IsDir() {
		if node.IsTarget() {
			outFile = filepath.Join(outFile, "ais"+apc.Target+"-"+node.ID())
		} else {
			outFile = filepath.Join(outFile, "ais"+apc.Proxy+"-"+node.ID())
		}
		// TODO: strictly, fstat again and confirm
	} else if finfo.Mode().IsRegular() && !flagIsSet(c, yesFlag) {
		warn := fmt.Sprintf("overwrite existing %q", outFile)
		if ok := confirm(c, warn); !ok {
			return outFile, false
		}
	}
	return outFile, true
}
