// Package updatecheck compares the running build against the latest published
// release. The check is on demand and never automatic: a self-hosted deployment
// does not phone home unless an operator asks it to, and an operator who wants
// no outbound call at all can turn the feature off entirely.
package updatecheck

import (
	"regexp"
	"strconv"
	"strings"
)

// numericPrefix splits "1.2.3-rc1" into the dotted numbers and the remainder.
var numericPrefix = regexp.MustCompile(`^(\d+(?:\.\d+)*)(.*)$`)

// parsed is a version split into its numeric parts and its pre-release suffix.
type parsed struct {
	numbers []int
	pre     string
}

// parse reads a version the way the tags in this project are written: an
// optional "v", dotted numbers, and an optional pre-release suffix separated by
// a dot, dash or underscore. Anything it cannot read becomes version 0, which
// compares below every real release and therefore never claims an update.
func parse(raw string) parsed {
	text := strings.TrimSpace(raw)
	text = strings.TrimPrefix(strings.TrimPrefix(text, "v"), "V")
	m := numericPrefix.FindStringSubmatch(text)
	if m == nil {
		return parsed{numbers: []int{0}}
	}
	fields := strings.Split(m[1], ".")
	numbers := make([]int, 0, len(fields))
	for _, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil {
			n = 0
		}
		numbers = append(numbers, n)
	}
	return parsed{numbers: numbers, pre: strings.Trim(m[2], ".-_ ")}
}

// Compare orders two versions: -1 when a is older than b, 0 when they are the
// same release, and 1 when a is newer.
//
// Missing numeric components are zero, so 1.2 and 1.2.0 are the same release. A
// pre-release is older than the release it leads to (1.2.0-rc1 < 1.2.0), which
// is the rule that keeps a release candidate from being offered as an upgrade
// over the final build. Two different pre-releases of the same version are
// ordered lexicographically, which is right for rc1 < rc2 and arbitrary but
// harmless otherwise.
func Compare(a, b string) int {
	x, y := parse(a), parse(b)
	width := max(len(x.numbers), len(y.numbers))
	for i := range width {
		l, r := 0, 0
		if i < len(x.numbers) {
			l = x.numbers[i]
		}
		if i < len(y.numbers) {
			r = y.numbers[i]
		}
		if l != r {
			if l > r {
				return 1
			}
			return -1
		}
	}
	switch {
	case x.pre == y.pre:
		return 0
	case x.pre == "": // a is the release, b is a pre-release of it
		return 1
	case y.pre == "":
		return -1
	case x.pre > y.pre:
		return 1
	default:
		return -1
	}
}

// IsNewer reports whether latest is a strictly newer release than current.
//
// An unknown current version — a development build that carries no tag — is
// never told it is out of date: the operator running it built it themselves and
// a release number means nothing to them.
func IsNewer(current, latest string) bool {
	if latest == "" || !known(current) {
		return false
	}
	return Compare(latest, current) > 0
}

// known reports whether a version string names a release rather than a
// development build.
func known(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" || v == "dev" || strings.HasPrefix(v, "dev-") {
		return false
	}
	return numericPrefix.MatchString(strings.TrimPrefix(strings.TrimPrefix(v, "v"), "V"))
}
