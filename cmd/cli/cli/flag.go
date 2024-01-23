// Package cli provides easy-to-use commands to manage, monitor, and utilize AIS clusters.
// This file contains util functions and types.
/*
 * Copyright (c) 2018-2023, NVIDIA CORPORATION. All rights reserved.
 */
package cli

import (
	"flag"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/NVIDIA/aistore/cmd/cli/teb"
	"github.com/NVIDIA/aistore/cmn/cos"
	"github.com/urfave/cli"
)

type (
	DurationFlag    cli.DurationFlag
	DurationFlagVar cli.DurationFlag
)

// interface guards
var (
	_ flag.Value = &DurationFlagVar{}
	_ cli.Flag   = &DurationFlag{}
)

/////////////////////
// DurationFlagVar //
/////////////////////

// "s" (seconds) is the default time unit
func (f *DurationFlagVar) Set(s string) (err error) {
	if _, err := strconv.ParseInt(s, 10, 64); err == nil {
		s += "s"
	}
	f.Value, err = time.ParseDuration(s)
	return err
}

//nolint:gocritic // ignoring hugeParam - following the orig. github.com/urfave style
func (f DurationFlagVar) String() string {
	return f.Value.String() // compare with orig. DurationFlag.String()
}

//////////////////
// DurationFlag //
//////////////////

//nolint:gocritic // ignoring hugeParam - following the orig. github.com/urfave style
func (f DurationFlag) ApplyWithError(set *flag.FlagSet) error {
	// construction via `FlagSet.Var` to override duration-parsing default
	fvar := DurationFlagVar(f)
	parts := splitCsv(f.Name)
	for _, name := range parts {
		name = strings.Trim(name, " ")
		set.Var(&fvar, name, f.Usage)
	}
	return nil
}

//nolint:gocritic // ignoring hugeParam - following the orig. github.com/urfave style
func (f DurationFlag) String() string {
	// compare with DurationFlagVar.String()
	s := cli.FlagStringer(f)

	// TODO: remove the " (default: ...)" suffix - it only makes sense when actually supported
	re := regexp.MustCompile(` \(default: \S+\)$`)
	if loc := re.FindStringIndex(s); loc != nil {
		s = s[:loc[0]]
	}
	return s
}

//nolint:gocritic // ignoring hugeParam - following the orig. github.com/urfave style
func (f DurationFlag) GetName() string { return f.Name }

//nolint:gocritic // ignoring hugeParam - following the orig. github.com/urfave style
func (f DurationFlag) Apply(set *flag.FlagSet) { _ = f.ApplyWithError(set) }

//
// flag parsers & misc. helpers
//

// flag's printable name
func flprn(f cli.Flag) string { return flagPrefix + fl1n(f.GetName()) }

// in single quotes
func qflprn(f cli.Flag) string { return "'" + flprn(f) + "'" }

// return the first name
func fl1n(flagName string) string {
	if strings.IndexByte(flagName, ',') < 0 {
		return flagName
	}
	l := splitCsv(flagName)
	return l[0]
}

func flagIsSet(c *cli.Context, flag cli.Flag) (v bool) {
	name := fl1n(flag.GetName()) // take the first of multiple names
	switch flag.(type) {
	case cli.BoolFlag:
		v = c.Bool(name)
	case cli.BoolTFlag:
		v = c.BoolT(name)
	default:
		v = c.GlobalIsSet(name) || c.IsSet(name)
	}
	return
}

// Returns the value of a string flag (either parent or local scope - here and elsewhere)
func parseStrFlag(c *cli.Context, flag cli.Flag) string {
	flagName := fl1n(flag.GetName())
	if c.GlobalIsSet(flagName) {
		return c.GlobalString(flagName)
	}
	return c.String(flagName)
}

//nolint:gocritic // ignoring hugeParam - following the orig. github.com/urfave style
func parseIntFlag(c *cli.Context, flag cli.IntFlag) int {
	flagName := fl1n(flag.GetName())
	if c.GlobalIsSet(flagName) {
		return c.GlobalInt(flagName)
	}
	return c.Int(flagName)
}

func parseDurationFlag(c *cli.Context, flag cli.Flag) time.Duration {
	flagName := fl1n(flag.GetName())
	if c.GlobalIsSet(flagName) {
		return c.GlobalDuration(flagName)
	}
	return c.Duration(flagName)
}

//nolint:gocritic // ignoring hugeParam - following the orig. github.com/urfave style
func parseUnitsFlag(c *cli.Context, flag cli.StringFlag) (units string, err error) {
	units = parseStrFlag(c, flag) // enum { unitsSI, ... }
	if err = teb.ValidateUnits(units); err != nil {
		err = fmt.Errorf("%s=%s is invalid: %v", flprn(flag), units, err)
	}
	return
}

//nolint:gocritic // ignoring hugeParam - following the orig. github.com/urfave style
func parseSizeFlag(c *cli.Context, flag cli.StringFlag, unitsParsed ...string) (int64, error) {
	var (
		err   error
		units string
		val   = parseStrFlag(c, flag)
	)
	if len(unitsParsed) > 0 {
		units = unitsParsed[0]
	} else if flagIsSet(c, unitsFlag) {
		units, err = parseUnitsFlag(c, unitsFlag)
		if err != nil {
			return 0, err
		}
	}
	return cos.ParseSize(val, units)
}

func rmFlags(flags []cli.Flag, fs ...cli.Flag) (out []cli.Flag) {
	out = make([]cli.Flag, 0, len(flags))
loop:
	for _, flag := range flags {
		for _, f := range fs {
			if flag.GetName() == f.GetName() {
				continue loop
			}
		}
		out = append(out, flag)
	}
	return
}
