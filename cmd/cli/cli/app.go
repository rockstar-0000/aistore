// Package cli provides easy-to-use commands to manage, monitor, and utilize AIS clusters.
/*
 * Copyright (c) 2018-2024, NVIDIA CORPORATION. All rights reserved.
 */
package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/NVIDIA/aistore/cmd/cli/config"
	"github.com/NVIDIA/aistore/cmd/cli/teb"
	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/cmn/debug"
	"github.com/fatih/color"
	"github.com/urfave/cli"
)

const (
	cliName  = "ais"
	ua       = "ais/cli"
	metadata = "md"
)

const (
	cliDescr = `If <TAB-TAB> completion doesn't work:
   * download ` + cmn.GitHubHome + `/tree/main/cmd/cli/autocomplete
   * run 'cmd/cli/autocomplete/install.sh'
   To install CLI directly from GitHub: ` + cmn.GitHubHome + `/blob/main/deploy/scripts/install_from_binaries.sh`

	// custom cli.AppHelpTemplate
	// "You can render custom help text by setting this variable." (from github.com/urfave/cli)
	appHelpTemplate = `NAME:
   {{.Name}}{{if .Usage}} - {{.Usage}}{{end}}

USAGE:
   {{if .UsageText}}{{.UsageText}}{{else}}{{.HelpName}} {{if .VisibleFlags}}[global options]{{end}}{{if .Commands}} command [command options]{{end}} {{if .ArgsUsage}}{{.ArgsUsage}}{{else}}[arguments...]{{end}}{{end}}{{if .Version}}{{if not .HideVersion}}

VERSION:
   {{.Version}}{{end}}{{end}}{{if .Description}}

TAB completions (Bash and Zsh):
   {{.Description}}{{end}}{{if len .Authors}}

AUTHOR{{with $length := len .Authors}}{{if ne 1 $length}}S{{end}}{{end}}:
   {{range $index, $author := .Authors}}{{if $index}}
   {{end}}{{$author}}{{end}}{{end}}{{if .VisibleCommands}}

COMMANDS:{{range .VisibleCategories}}{{if .Name}}

   {{.Name}}:{{range .VisibleCommands}}
     {{join .Names ", "}}{{"\t"}}{{.Usage}}{{end}}{{else}}{{range .VisibleCommands}}
   {{join .Names ", "}}{{"\t"}}{{.Usage}}{{end}}{{end}}{{end}}{{end}}{{if .VisibleFlags}}

GLOBAL OPTIONS:
   {{range $index, $option := .VisibleFlags}}{{if $index}}
   {{end}}{{$option}}{{end}}{{end}}{{if .Copyright}}

COPYRIGHT:
   {{.Copyright}}{{end}}
`
)

const (
	// currently _required_ - e.g., `ais://mmm` cannot be reduced to `mmm`
	// in the future, we may fully support cfg.DefaultProvider but not yet
	// * see also: cmd/cli/config/config.go
	providerRequired = true
)

type (
	acli struct {
		app       *cli.App
		outWriter io.Writer
		errWriter io.Writer
		longRun   *longRun
	}
	longRun struct {
		count            int
		lfooter          int
		refreshRate      time.Duration
		offset           int64
		mapBegin, mapEnd teb.StstMap
		outFile          *os.File
	}
)

var (
	cfg         *config.Config
	buildTime   string
	k8sDetected bool
)

// color
var (
	fred, fcyan, fblue, fgreen func(a ...any) string
)

// `ais help [COMMAND]`
var helpCommand = cli.Command{
	Name:      "help",
	Usage:     "show a list of commands; show help for a given command",
	ArgsUsage: "[COMMAND]",
	Action:    helpCmdHandler,
	BashComplete: func(c *cli.Context) {
		for _, cmd := range c.App.Commands {
			fmt.Println(cmd.Name)
		}
	},
}

func cliConfVerbose() bool { return cfg.Verbose } // more warnings, errors with backtraces and details

func helpCmdHandler(c *cli.Context) error {
	args := c.Args()
	if args.Present() {
		return cli.ShowCommandHelp(c, args.First())
	}

	cli.ShowAppHelp(c)
	return nil
}

// main method
func Run(version, buildtime string, args []string) error {
	a := acli{app: cli.NewApp(), outWriter: os.Stdout, errWriter: os.Stderr, longRun: &longRun{}}
	buildTime = buildtime

	// empty command line or 'ais help'
	debug.Assert(args[0] == cliName, "expecting arg0:", cliName)
	emptyCmdline := len(args) == 1 ||
		strings.Contains(args[1], "help") ||
		strings.Contains(args[1], "usage") ||
		args[1] == "-h" ||
		strings.Contains(args[1], fl1n(cli.HelpFlag.GetName()))

	a.init(version, emptyCmdline)

	teb.Init(os.Stdout, cfg.NoColor)

	// run
	if err := a.runOnce(args); err != nil {
		return err
	}
	if !a.longRun.isSet() {
		if emptyCmdline {
			fmt.Println("\nALIASES:")
			fmt.Println(indent1 + cfg.Aliases.Str(indent1))
		}
		return nil
	}
	if a.longRun.outFile != nil {
		defer a.longRun.outFile.Close()
	}
	if a.longRun.isForever() {
		return a.runForever(args)
	}
	return a.runN(args)
}

func (a *acli) runOnce(args []string) error {
	err := a.app.Run(args)
	if err == nil {
		return nil
	}
	if isStartingUp(err) {
		for i := 0; i < 4; i++ {
			briefPause(2)
			fmt.Fprint(a.app.Writer, ". ")
			if err = a.app.Run(args); err == nil {
				fmt.Fprintln(a.app.Writer)
				break
			}
		}
	}
	return formatErr(err)
}

func (a *acli) runForever(args []string) error {
	rate := a.longRun.refreshRate
	for {
		time.Sleep(rate)
		printLongRunFooter(a.outWriter, a.longRun.lfooter)
		if err := a.runOnce(args); err != nil {
			return err
		}
		a.longRun.mapBegin = a.longRun.mapEnd
		a.longRun.mapEnd = nil
	}
}

func printLongRunFooter(w io.Writer, repeat int) {
	if repeat > 0 {
		fmt.Fprintln(w, fcyan(strings.Repeat("-", repeat)))
	}
}

func (a *acli) runN(args []string) error {
	delim := fcyan(strings.Repeat("-", 16))
	fmt.Fprintln(a.outWriter, delim)
	for i := 2; i <= a.longRun.count; i++ {
		time.Sleep(a.longRun.refreshRate)
		if err := a.runOnce(args); err != nil {
			return err
		}
		if i < a.longRun.count {
			fmt.Fprintln(a.outWriter, delim)
		}
	}
	return nil
}

func (a *acli) init(version string, emptyCmdline bool) {
	app := a.app

	if cfg.NoColor {
		fcyan = fmt.Sprint
		fred = fmt.Sprint
		fblue = fmt.Sprint
		fgreen = fmt.Sprint
	} else {
		fcyan = color.New(color.FgHiCyan).SprintFunc()
		fred = color.New(color.FgHiRed).SprintFunc()
		fblue = color.New(color.FgHiBlue).SprintFunc()
		fgreen = color.New(color.FgHiGreen).SprintFunc()
	}

	app.Name = cliName
	app.Usage = "AIS CLI: command-line management utility for AIStore"
	app.Version = version
	app.EnableBashCompletion = true
	app.HideHelp = true
	app.Flags = []cli.Flag{cli.HelpFlag}
	app.CommandNotFound = commandNotFoundHandler
	app.OnUsageError = onUsageErrorHandler
	app.Metadata = map[string]any{metadata: a.longRun}
	app.Writer = a.outWriter
	app.ErrWriter = a.errWriter
	app.Description = cliDescr

	cli.AppHelpTemplate = appHelpTemplate

	a.setupCommands(emptyCmdline)
}

func (a *acli) setupCommands(emptyCmdline bool) {
	app := a.app

	// Note: order of commands below is the order shown in "ais help"
	appendJobSub(&jobCmd)
	app.Commands = []cli.Command{
		bucketCmd,
		objectCmd,
		clusterCmd,
		configCmd,
		etlCmd,
		jobCmd,
		authCmd,
		showCmd,
		helpCommand,
		advancedCmd,
		storageCmd,
		archCmd,
		logCmd,
		perfCmd,
		remClusterCmd,
		a.getAliasCmd(),
	}

	if k8sDetected {
		app.Commands = append(app.Commands, k8sCmd)
	}

	// not adding aliases - showing them as part of `ais [--help]`
	if emptyCmdline {
		return
	}

	app.Commands = append(app.Commands, a.initAliases()...)
	setupCommandHelp(app.Commands)
	a.enableSearch()
}

func (a *acli) enableSearch() {
	initSearch(a.app)
	a.app.Commands = append(a.app.Commands, searchCommands...)
}

func setupCommandHelp(commands []cli.Command) {
	lst := splitCsv(cli.HelpFlag.GetName())
	helpName := lst[0]
	for i := range commands {
		command := &commands[i]

		// Get rid of 'h'/'help' subcommands
		// and add the help flag manually
		command.HideHelp = true
		// (but only if there isn't one already)
		if !hasHelpFlag(command.Flags, helpName) {
			command.Flags = append(command.Flags, cli.HelpFlag)
		}
		command.OnUsageError = onUsageErrorHandler

		// recursively
		setupCommandHelp(command.Subcommands)
	}
}

func hasHelpFlag(commandFlags []cli.Flag, helpName string) bool {
	for _, flag := range commandFlags {
		lst := splitCsv(flag.GetName())
		for _, name := range lst {
			if name == helpName {
				return true
			}
		}
	}
	return false
}

//
// cli.App error callbacks
//

func commandNotFoundHandler(c *cli.Context, cmd string) {
	if cmd == "version" {
		fmt.Fprintf(c.App.Writer, "version %s (build %s)\n", c.App.Version, buildTime)
		return
	}
	err := commandNotFoundError(c, cmd)
	fmt.Fprint(c.App.ErrWriter, err)
	os.Exit(1)
}

func onUsageErrorHandler(c *cli.Context, err error, _ bool) error {
	if c == nil {
		return err
	}
	return cannotExecuteError(c, err, "")
}

/////////////
// longRun //
/////////////

func (p *longRun) isForever() bool {
	return p.count == countUnlimited
}

func (p *longRun) isSet() bool {
	return p.refreshRate != 0
}

func (p *longRun) init(c *cli.Context, runOnce bool) {
	if flagIsSet(c, refreshFlag) {
		p.refreshRate = parseDurationFlag(c, refreshFlag)
		p.count = countUnlimited // unless counted (below)
	} else if runOnce {
		p.count = 1 // unless --count spec-ed (below)
	}
	if flagIsSet(c, countFlag) {
		p.count = parseIntFlag(c, countFlag)
		if p.count <= 0 {
			n := flprn(countFlag)
			warn := fmt.Sprintf("option '%s=%d' is invalid (must be >= 1). Proceeding with '%s=%d' (default).",
				n, p.count, n, countDefault)
			actionWarn(c, warn)
			briefPause(2)
			p.count = countDefault
		}
	}
}

func isLongRun(c *cli.Context) bool {
	params := c.App.Metadata[metadata].(*longRun)
	return params.isSet()
}

func setLongRunParams(c *cli.Context, footer ...int) bool {
	params := c.App.Metadata[metadata].(*longRun)
	if params.isSet() {
		return false
	}
	params.lfooter = 8
	if len(footer) > 0 {
		params.lfooter = footer[0]
	}
	params.init(c, false)
	return true
}

func getLongRunParams(c *cli.Context) *longRun {
	params := c.App.Metadata[metadata].(*longRun)
	if !params.isSet() {
		return nil
	}
	return params
}

func addLongRunOffset(c *cli.Context, off int64) {
	params := c.App.Metadata[metadata].(*longRun)
	if params.isSet() {
		params.offset += off
	}
}

func getLongRunOffset(c *cli.Context) int64 {
	params := c.App.Metadata[metadata].(*longRun)
	return params.offset
}

func setLongRunOutfile(c *cli.Context, file *os.File) {
	params := c.App.Metadata[metadata].(*longRun)
	if params.isSet() {
		params.outFile = file
	}
}

func getLongRunOutfile(c *cli.Context) *os.File {
	params := c.App.Metadata[metadata].(*longRun)
	return params.outFile
}
