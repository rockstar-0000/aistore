// Package cli provides easy-to-use commands to manage, monitor, and utilize AIS clusters.
/*
 * Copyright (c) 2018-2023, NVIDIA CORPORATION. All rights reserved.
 */
package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/NVIDIA/aistore/api"
	"github.com/NVIDIA/aistore/api/apc"
	"github.com/NVIDIA/aistore/cmn"
	"github.com/NVIDIA/aistore/cmn/cos"
	"github.com/urfave/cli"
)

// generic types
type (
	here struct {
		arg     string
		abspath string
		finfo   os.FileInfo
		fdnames []string // files and directories (names)
		isdir   bool
		recurs  bool
		stdin   bool
	}
	there struct {
		bck   cmn.Bck
		oname string
		lr    apc.ListRange
	}
)

// assorted specific
type (
	wop interface {
		verb() string
		dest() string
	}
	// PUT object(s)
	putargs struct {
		src here
		pt  *cos.ParsedTemplate // client-side, via --list|template or src/range
		dst there
	}
	// 'ais archive bucket'
	archbck struct {
		putargs
		rsrc        there
		apndIfExist bool
	}
	// 'ais archive put' (with an option to append)
	archput struct {
		putargs
		archpath    string
		appendOnly  bool
		appendOrPut bool
	}
)

// interface guard
var (
	_ wop = (*putargs)(nil)
	_ wop = (*archbck)(nil)
	_ wop = (*archput)(nil)
)

func (*putargs) verb() string { return "PUT" }

func (a *putargs) dest() string       { return a.dst.bck.Cname("") }
func (a *putargs) srcIsRegular() bool { return a.src.finfo != nil && !a.src.isdir }

func (a *putargs) parse(c *cli.Context, emptyDstOnameOK bool) (err error) {
	if c.NArg() == 0 {
		return missingArgumentsError(c, c.Command.ArgsUsage)
	}
	if flagIsSet(c, listFlag) && flagIsSet(c, templateFlag) {
		return incorrectUsageMsg(c, errFmtExclusive, qflprn(listFlag), qflprn(templateFlag))
	}
	if flagIsSet(c, progressFlag) || flagIsSet(c, listFlag) || flagIsSet(c, templateFlag) {
		// check connectivity (since '--progress' steals STDOUT with multi-object producing
		// scary looking errors when there's no cluster)
		if _, err = api.GetClusterMap(apiBP); err != nil {
			return
		}
	}
	switch {
	case c.NArg() == 1: // BUCKET/[OBJECT_NAME] --list|--template
		uri := c.Args().Get(0) // dst
		a.dst.bck, a.dst.oname, err = parseBckObjURI(c, uri, emptyDstOnameOK)
		if err != nil {
			return
		}
		// src via local filenames
		if !flagIsSet(c, listFlag) && !flagIsSet(c, templateFlag) {
			return fmt.Errorf("missing source arg in %q", c.Command.ArgsUsage)
		}
		if flagIsSet(c, listFlag) {
			csv := parseStrFlag(c, listFlag)
			a.src.fdnames = splitCsv(csv)
			return
		}
		// optional template to select local source(s)
		var (
			pt   cos.ParsedTemplate
			tmpl = parseStrFlag(c, templateFlag)
		)
		pt, err = cos.NewParsedTemplate(tmpl)
		if err == nil {
			a.pt = &pt
		}
		return

	case c.NArg() == 2: // FILE|DIRECTORY|DIRECTORY/PATTERN   BUCKET/[OBJECT_NAME]
		a.src.arg = c.Args().Get(0) // src
		uri := c.Args().Get(1)      // dst

		a.dst.bck, a.dst.oname, err = parseBckObjURI(c, uri, emptyDstOnameOK)
		if err != nil {
			return err
		}

		const efmt = "source (%q) and flag (%s) cannot are mutually exclusive"
		if flagIsSet(c, listFlag) {
			return fmt.Errorf(efmt, a.src.arg, qflprn(listFlag))
		}
		if flagIsSet(c, templateFlag) {
			return fmt.Errorf(efmt, a.src.arg, qflprn(templateFlag))
		}

		// STDIN
		if a.src.arg == "-" {
			a.src.stdin = true
			if a.dst.oname == "" {
				err = fmt.Errorf("missing destination object name (in %s) - required when writing directly from standard input",
					c.Command.ArgsUsage)
			}
			return
		}
		// file or files
		if a.src.abspath, err = absPath(a.src.arg); err != nil {
			return
		}
		// inline "range" w/ no flag, e.g.: "/tmp/www/test{0..2}{0..2}.txt" ais://nnn/www
		pt, errV := cos.ParseBashTemplate(a.src.abspath)
		if errV == nil {
			a.pt = &pt
			return
		}
		// local file or dir?
		finfo, errV := os.Stat(a.src.abspath)
		if errV != nil {
			// must be a csv list of files embedded with the first arg
			a.src.fdnames = splitCsv(a.src.arg)
			return
		}

		a.src.finfo = finfo
		// reg file
		if !finfo.IsDir() {
			if a.dst.oname == "" {
				// PUT [convention]: use `basename` as the destination object name, unless specified
				a.dst.oname = filepath.Base(a.src.abspath)
			}
			return
		}
		// finally: a local (or rather, client-accessible) directory
		a.src.isdir = true
		a.src.recurs = flagIsSet(c, recursFlag)
		return
	}

	if err := mistypedFlag(c.Args()[2:]); err != nil {
		return err
	}

	const (
		efmt = "too many arguments: '%s'"
		hint = "(hint: wildcards must be in single or double quotes, see `--help` for details)"
	)
	l := c.NArg()
	if l > 4 {
		return fmt.Errorf(efmt+" ...\n%s\n", strings.Join(c.Args()[2:4], " "), hint)
	}
	return fmt.Errorf(efmt+"\n%s\n", strings.Join(c.Args()[2:], " "), hint)
}

func (*archbck) verb() string { return "ARCHIVE" }

func (a *archbck) dest() string { return a.dst.bck.Cname(a.dst.oname) }

func (a *archbck) parse(c *cli.Context) (err error) {
	err = a.putargs.parse(c, false /*empty dst oname ok*/)
	if a.dst.bck.IsEmpty() || err == nil {
		return
	}
	//
	// parse a.rsrc (TODO -- FIXME: support archiving local a.src)
	//
	if !flagIsSet(c, listFlag) && !flagIsSet(c, templateFlag) {
		return missingArgumentsError(c,
			fmt.Sprintf("either a list of object names via %s or selection template (%s)",
				flprn(listFlag), flprn(templateFlag)))
	}
	if flagIsSet(c, listFlag) && flagIsSet(c, templateFlag) {
		return incorrectUsageMsg(c, fmt.Sprintf("%s and %s options are mutually exclusive",
			flprn(listFlag), flprn(templateFlag)))
	}
	uri := c.Args().Get(0) // remote source
	if a.rsrc.bck, err = parseBckURI(c, uri, false); err != nil {
		return
	}
	if flagIsSet(c, listFlag) {
		list := parseStrFlag(c, listFlag)
		a.rsrc.lr.ObjNames = splitCsv(list)
	} else {
		a.rsrc.lr.Template = parseStrFlag(c, templateFlag)
	}
	return
}

func (*archput) verb() string { return "APPEND" }

func (a *archput) dest() string { return a.dst.bck.Cname(a.dst.oname) }

func (a *archput) parse(c *cli.Context) (err error) {
	a.archpath = parseStrFlag(c, archpathFlag)
	a.appendOnly = flagIsSet(c, archAppendOnlyFlag)
	a.appendOrPut = flagIsSet(c, archAppendOrPutFlag)
	if a.appendOnly && a.appendOrPut {
		return incorrectUsageMsg(c, errFmtExclusive, qflprn(archAppendOnlyFlag), qflprn(archAppendOrPutFlag))
	}
	err = a.putargs.parse(c, false /*empty dst oname ok*/)
	return
}
