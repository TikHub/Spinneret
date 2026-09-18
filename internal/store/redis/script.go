package redis

import (
	"context"
	"crypto/sha1" //nolint:gosec // SHA-1 is mandated by EVALSHA; it is not used for security.
	_ "embed"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"

	"github.com/redis/rueidis"
)

// CommonLua is the Lua prelude library prepended to every script registered
// through NewScript. It defines the sp_* helpers (key base derivation, packed
// health state codec, score math, availability rule) shared by all owners.
//
// Each helper is introduced by a "--@sp <name>" marker line; NewScript emits
// only the helpers a body actually uses (see preludeFor).
//
//go:embed lua/common.lua
var CommonLua string

// Script is a Lua script whose source is the prelude a body needs followed by
// that body. It is immutable and safe for concurrent use.
type Script struct {
	// Name identifies the script in logs and errors (e.g. "acquire").
	Name string

	source string
	sha1   string
	lua    *rueidis.Lua
}

// preludeSection is one helper of CommonLua: its name, its source text and the
// helpers it calls itself.
type preludeSection struct {
	name string
	src  string
	deps []string
}

// preludeHeader is everything above the first "--@sp" marker of CommonLua (the
// file comment). preludeSections are the helpers in file order, which is also
// definition order: a helper only depends on helpers declared before it.
var (
	preludeHeader   string
	preludeSections []preludeSection
	preludeByName   map[string]int
)

// spNameRe matches an sp_* identifier.
var spNameRe = regexp.MustCompile(`\bsp_[a-z0-9_]+`)

func init() {
	parsePrelude(CommonLua)
}

// parsePrelude splits src on the "--@sp <name>" marker lines and records each
// section's dependencies. It panics on a malformed library, which is a build
// error in this repository rather than a runtime condition.
func parsePrelude(src string) {
	preludeHeader = ""
	preludeSections = nil
	preludeByName = map[string]int{}

	const marker = "--@sp "
	lines := strings.Split(src, "\n")
	var cur *preludeSection
	var header, body strings.Builder
	flush := func() {
		if cur == nil {
			return
		}
		cur.src = body.String()
		cur.deps = declaredDeps(cur.name, cur.src)
		preludeByName[cur.name] = len(preludeSections)
		preludeSections = append(preludeSections, *cur)
		body.Reset()
	}
	for _, line := range lines {
		if name, ok := markerName(line, marker); ok {
			flush()
			cur = &preludeSection{name: name}
			continue
		}
		if cur == nil {
			header.WriteString(line)
			header.WriteByte('\n')
			continue
		}
		body.WriteString(line)
		body.WriteByte('\n')
	}
	flush()
	preludeHeader = header.String()
	if len(preludeSections) == 0 {
		panic("redis: lua/common.lua declares no --@sp helper section")
	}
	for _, s := range preludeSections {
		if !strings.Contains(s.src, "local function "+s.name+"(") {
			panic("redis: lua/common.lua section " + s.name + " does not declare it")
		}
	}
}

// markerName returns the helper name of a "--@sp <name>" line.
func markerName(line, marker string) (string, bool) {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, marker) {
		return "", false
	}
	name := strings.TrimSpace(strings.TrimPrefix(t, marker))
	if name == "" {
		panic("redis: lua/common.lua has an unnamed --@sp marker")
	}
	return name, true
}

// declaredDeps returns the sp_* helper names the code of src references,
// excluding its own. Comments are stripped first: every helper documents its
// neighbours, and a doc reference is not a dependency.
func declaredDeps(self, src string) []string {
	seen := map[string]bool{self: true}
	var out []string
	for _, m := range spNameRe.FindAllString(stripLuaComments(src), -1) {
		if seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// stripLuaComments replaces the comments of a Lua chunk with spaces, leaving
// string literals (including long literals) untouched, so that scanning the
// result finds identifiers that are really called. Newlines are preserved so
// positions and line counts stay comparable.
func stripLuaComments(src string) string {
	out := []byte(src)
	blank := func(from, to int) {
		for i := from; i < to && i < len(out); i++ {
			if out[i] != '\n' {
				out[i] = ' '
			}
		}
	}
	i, n := 0, len(src)
	for i < n {
		switch c := src[i]; {
		case c == '-' && i+1 < n && src[i+1] == '-':
			if lvl, ok := longBracket(src, i+2); ok {
				end := longBracketEnd(src, i+2+lvl+2, lvl)
				blank(i, end)
				i = end
				continue
			}
			end := i
			for end < n && src[end] != '\n' {
				end++
			}
			blank(i, end)
			i = end
		case c == '\'' || c == '"':
			i = shortStringEnd(src, i)
		case c == '[':
			if lvl, ok := longBracket(src, i); ok {
				i = longBracketEnd(src, i+lvl+2, lvl)
				continue
			}
			i++
		default:
			i++
		}
	}
	return string(out)
}

// longBracket reports whether src[at:] opens a Lua long bracket "[==[" and
// returns the number of '=' signs of its level.
func longBracket(src string, at int) (int, bool) {
	if at >= len(src) || src[at] != '[' {
		return 0, false
	}
	lvl := 0
	for at+1+lvl < len(src) && src[at+1+lvl] == '=' {
		lvl++
	}
	if at+1+lvl < len(src) && src[at+1+lvl] == '[' {
		return lvl, true
	}
	return 0, false
}

// longBracketEnd returns the index just past the closing bracket of a long
// bracket of the given level that starts at from, or len(src) when unterminated.
func longBracketEnd(src string, from, lvl int) int {
	closing := "]" + strings.Repeat("=", lvl) + "]"
	if idx := strings.Index(src[from:], closing); idx >= 0 {
		return from + idx + len(closing)
	}
	return len(src)
}

// shortStringEnd returns the index just past the quoted literal starting at at.
func shortStringEnd(src string, at int) int {
	quote := src[at]
	i := at + 1
	for i < len(src) {
		switch src[i] {
		case '\\':
			i += 2
			continue
		case quote:
			return i + 1
		case '\n':
			return i
		}
		i++
	}
	return len(src)
}

// preludeFor returns the prelude for body: the file header followed by every
// helper body names, transitively, in file order. Names that are not helpers
// (a local function of the body itself, a comment) are ignored, and a body that
// names no helper still gets the header, so the emitted source never depends on
// anything but the body text.
func preludeFor(body string) string {
	need := make([]bool, len(preludeSections))
	var mark func(name string)
	mark = func(name string) {
		idx, ok := preludeByName[name]
		if !ok || need[idx] {
			return
		}
		need[idx] = true
		for _, d := range preludeSections[idx].deps {
			mark(d)
		}
	}
	for _, m := range spNameRe.FindAllString(stripLuaComments(body), -1) {
		mark(m)
	}

	var b strings.Builder
	b.Grow(len(CommonLua))
	b.WriteString(preludeHeader)
	for i, s := range preludeSections {
		if need[i] {
			b.WriteString(s.src)
		}
	}
	return b.String()
}

// NewScript registers a script named name whose source is the common prelude
// followed by body. Helpers from the prelude are file-level locals, so body can
// call them directly (Redis forbids scripts from defining globals). Only the
// helpers body uses are emitted: creating a chunk-level closure costs about
// 0.14 us per script call, which a two-helper script must not pay eighteen
// times.
func NewScript(name, body string) *Script {
	label := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' {
			return ' '
		}
		return r
	}, name)
	source := preludeFor(body) + "\n-- ==== script: " + label + " ====\n" + body
	sum := sha1.Sum([]byte(source)) //nolint:gosec // EVALSHA digest, not a security primitive.
	return &Script{
		Name:   name,
		source: source,
		sha1:   hex.EncodeToString(sum[:]),
		lua:    rueidis.NewLuaScript(source),
	}
}

// Source returns the full script source (prelude + body).
func (s *Script) Source() string {
	return s.source
}

// SHA1 returns the lowercase hex SHA-1 digest used with EVALSHA.
func (s *Script) SHA1() string {
	return s.sha1
}

// Exec runs the script with EVALSHA and transparently falls back to EVAL when
// the server answers NOSCRIPT (for example after a restart or SCRIPT FLUSH).
// In cluster mode all keys must hash to the same slot.
func (s *Script) Exec(ctx context.Context, c rueidis.Client, keys, args []string) rueidis.RedisResult {
	return s.lua.Exec(ctx, c, keys, args)
}

// ExecMulti runs the script once per call in a single pipeline. Every call is
// first attempted with EVALSHA; only the calls that fail with NOSCRIPT are
// resent with EVAL (which also caches the script on the node), so the common
// path costs a single round trip. Results are returned in call order, but
// calls that needed the EVAL fallback execute after the others, so callers
// must not rely on execution order between calls touching the same keys.
func (s *Script) ExecMulti(ctx context.Context, c rueidis.Client, calls ...rueidis.LuaExec) []rueidis.RedisResult {
	if len(calls) == 0 {
		return nil
	}
	cmds := make(rueidis.Commands, len(calls))
	for i, call := range calls {
		cmds[i] = c.B().Evalsha().Sha1(s.sha1).Numkeys(int64(len(call.Keys))).Key(call.Keys...).Arg(call.Args...).Build()
	}
	results := c.DoMulti(ctx, cmds...)

	var missing []int
	for i, r := range results {
		if rerr, ok := rueidis.IsRedisErr(r.Error()); ok && rerr.IsNoScript() {
			missing = append(missing, i)
		}
	}
	if len(missing) == 0 {
		return results
	}

	retry := make(rueidis.Commands, len(missing))
	for j, i := range missing {
		call := calls[i]
		retry[j] = c.B().Eval().Script(s.source).Numkeys(int64(len(call.Keys))).Key(call.Keys...).Arg(call.Args...).Build()
	}
	for j, r := range c.DoMulti(ctx, retry...) {
		results[missing[j]] = r
	}
	return results
}
