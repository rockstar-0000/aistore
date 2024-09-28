// Package cos provides common low-level types and utilities for all aistore projects
/*
 * Copyright (c) 2018-2024, NVIDIA CORPORATION. All rights reserved.
 */
package cos

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/NVIDIA/aistore/cmn/atomic"
	"github.com/OneOfOne/xxhash"
	"github.com/teris-io/shortid"
)

const (
	// Alphabet for generating UUIDs similar to the shortid.DEFAULT_ABC
	// NOTE: len(uuidABC) > 0x3f - see GenTie()
	uuidABC = "-5nZJDft6LuzsjGNpPwY7rQa39vehq4i1cV2FROo8yHSlC0BUEdWbIxMmTgKXAk_"
)

const (
	LenShortID    = 9 // UUID length, as per https://github.com/teris-io/shortid#id-length
	lenDaemonID   = 8 // min length, via cryptographic rand
	lenK8sProxyID = 13

	// NOTE: cannot be smaller than any of the valid max lengths - see above
	tooLongID = 32
)

// bucket name, remais alias
const (
	tooLongName = 64
)

const (
	mayOnlyContain = "may only contain letters, numbers, dashes (-), underscores (_)"
	OnlyNice       = "must be less than 32 characters and " + mayOnlyContain // NOTE tooLongID
	OnlyPlus       = mayOnlyContain + ", and dots (.)"
)

var (
	sid  *shortid.Shortid
	rtie atomic.Uint32
)

func InitShortID(seed uint64) {
	sid = shortid.MustNew(4 /*worker*/, uuidABC, seed)
}

//
// UUID
//

// compare with xreg.GenBEID
func GenUUID() (uuid string) {
	var h, t string
	uuid = sid.MustGenerate()
	if !isAlpha(uuid[0]) {
		tie := int(rtie.Add(1))
		h = string(rune('A' + tie%26))
	}
	c := uuid[len(uuid)-1]
	if c == '-' || c == '_' {
		tie := int(rtie.Add(1))
		t = string(rune('a' + tie%26))
	}
	return h + uuid + t
}

// "best-effort ID" - to independently and locally generate globally unique ID
// called by xreg.GenBEID
func GenBEID(val uint64, l int) string {
	b := make([]byte, l)
	for i := range l {
		if idx := int(val & letterIdxMask); idx < LenRunes {
			b[i] = LetterRunes[idx]
		} else {
			b[i] = LetterRunes[idx-LenRunes]
		}
		val >>= letterIdxBits
	}
	return UnsafeS(b)
}

func IsValidUUID(uuid string) bool {
	return len(uuid) >= LenShortID && IsAlphaNice(uuid)
}

//
// Daemon ID
//

func GenDaemonID() string { return CryptoRandS(lenDaemonID) }

func ValidateDaemonID(id string) error {
	if len(id) < lenDaemonID {
		return fmt.Errorf("node ID %q is too short", id)
	}
	if !IsAlphaNice(id) {
		return fmt.Errorf("node ID %q is invalid: must start with a letter, "+OnlyNice, id)
	}
	return nil
}

func HashK8sProxyID(nodeName string) (pid string) {
	digest := xxhash.Checksum64S(UnsafeB(nodeName), MLCG32)
	pid = strconv.FormatUint(digest, 36)
	if pid[0] >= '0' && pid[0] <= '9' {
		pid = pid[1:]
	}
	if l := lenK8sProxyID - len(pid); l > 0 {
		return GenBEID(digest, l) + pid
	}
	return pid
}

// (when config.TestingEnv)
func GenTestingDaemonID(suffix string) string {
	l := max(lenDaemonID-len(suffix), 3)
	return CryptoRandS(l) + suffix
}

//
// utility functions
//

func isAlpha(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// letters and numbers w/ '-' and '_' permitted with limitations (see OnlyNice const)
func IsAlphaNice(s string) bool {
	l := len(s)
	if l > tooLongID {
		return false
	}
	for i := range l {
		c := s[i]
		if isAlpha(c) || (c >= '0' && c <= '9') {
			continue
		}
		if c != '-' && c != '_' {
			return false
		}
		if i == 0 || i == l-1 {
			return false
		}
	}
	return true
}

// alpha-numeric++ including letters, numbers, dashes (-), and underscores (_)
// period (.) is allowed except for '..' (OnlyPlus const)
func CheckAlphaPlus(s, tag string) error {
	l := len(s)
	if l > tooLongName {
		return fmt.Errorf("%s is too long: %d > %d(max length)", tag, l, tooLongName)
	}
	for i := range l {
		c := s[i]
		if isAlpha(c) || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			continue
		}
		if c != '.' {
			return errors.New(tag + " is invalid: " + OnlyPlus)
		}
		if i < l-1 && s[i+1] == '.' {
			return errors.New(tag + " is invalid: " + OnlyPlus)
		}
	}
	return nil
}

// 3-letter tie breaker (fast)
func GenTie() string {
	tie := rtie.Add(1)
	b0 := uuidABC[tie&0x3f]
	b1 := uuidABC[-tie&0x3f]
	b2 := uuidABC[(tie>>2)&0x3f]
	return string([]byte{b0, b1, b2})
}
