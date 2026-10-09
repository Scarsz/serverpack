// Package mcver compares and matches Minecraft-style version strings
// (1.21.10, 1.21.11-rc3, 26.3, 26.3-rc-3, …).
package mcver

import (
	"strconv"
	"strings"
)

// parts splits v into its numeric release segments and the pre-release suffix (if any).
func parts(v string) (nums []int, pre string, ok bool) {
	release, pre, _ := strings.Cut(v, "-")
	for _, s := range strings.Split(release, ".") {
		n, err := strconv.Atoi(s)
		if err != nil {
			return nil, pre, false
		}
		nums = append(nums, n)
	}
	return nums, pre, true
}

// IsRelease reports whether v is a plain numeric release (no pre-release suffix).
func IsRelease(v string) bool {
	_, pre, ok := parts(v)
	return ok && pre == ""
}

// Compare returns -1, 0 or 1. Releases sort above their own pre-releases;
// unparseable versions sort below everything parseable and compare lexically among themselves.
func Compare(a, b string) int {
	an, apre, aok := parts(a)
	bn, bpre, bok := parts(b)
	switch {
	case !aok && !bok:
		return strings.Compare(a, b)
	case !aok:
		return -1
	case !bok:
		return 1
	}
	for i := 0; i < max(len(an), len(bn)); i++ {
		var x, y int
		if i < len(an) {
			x = an[i]
		}
		if i < len(bn) {
			y = bn[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	switch {
	case apre == bpre:
		return 0
	case apre == "":
		return 1
	case bpre == "":
		return -1
	}
	return comparePre(apre, bpre)
}

// comparePre compares pre-release suffixes, treating digit runs numerically (rc-10 > rc-9).
func comparePre(a, b string) int {
	for a != "" && b != "" {
		ad, arest := splitRun(a)
		bd, brest := splitRun(b)
		an, aerr := strconv.Atoi(ad)
		bn, berr := strconv.Atoi(bd)
		if aerr == nil && berr == nil {
			if an != bn {
				if an < bn {
					return -1
				}
				return 1
			}
		} else if c := strings.Compare(ad, bd); c != 0 {
			return c
		}
		a, b = arest, brest
	}
	return strings.Compare(a, b)
}

func splitRun(s string) (run, rest string) {
	isDigit := s[0] >= '0' && s[0] <= '9'
	i := 1
	for i < len(s) && (s[i] >= '0' && s[i] <= '9') == isDigit {
		i++
	}
	return s[:i], s[i:]
}

// Matches reports whether target satisfies spec. spec may be an exact version,
// a range "1.8-26.3" (inclusive), or a wildcard "1.21.x".
func Matches(target, spec string) bool {
	spec = strings.TrimSpace(spec)
	if spec == target {
		return true
	}
	if prefix, ok := strings.CutSuffix(spec, ".x"); ok {
		return target == prefix || strings.HasPrefix(target, prefix+".")
	}
	if lo, hi, ok := strings.Cut(spec, "-"); ok && IsRelease(lo) && IsRelease(hi) {
		return Compare(target, lo) >= 0 && Compare(target, hi) <= 0
	}
	return false
}

// MatchesAny reports whether target satisfies any of specs.
func MatchesAny(target string, specs []string) bool {
	for _, s := range specs {
		if Matches(target, s) {
			return true
		}
	}
	return false
}

// SameLine reports whether target falls under a coarse version such as "1.21"
// (used for best-effort hints like Spigot's tested versions).
func SameLine(target, coarse string) bool {
	return target == coarse || strings.HasPrefix(target, coarse+".")
}
