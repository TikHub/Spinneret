// Package durationx provides a human-friendly duration type used in policy
// specs and admin APIs. It extends time.Duration syntax with a "d" (day) unit
// and the "permanent" keyword.
package durationx

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Duration is a time.Duration that serializes as a canonical string.
// The value Permanent (-1) represents an unbounded duration.
type Duration time.Duration

// Permanent marks an unbounded duration (for example a permanent ban).
const Permanent Duration = -1

const (
	day         = 24 * time.Hour
	maxDuration = time.Duration(1<<63 - 1)
	// maxMillis is the largest millisecond count representable as a Duration.
	maxMillis = int64(maxDuration / time.Millisecond)
)

// Parse parses durations such as "500ms", "30s", "10m", "24h", "7d", "1h30m",
// "1d12h" and the keyword "permanent". An empty string or "0" is zero.
func Parse(s string) (Duration, error) {
	s = strings.TrimSpace(s)
	switch strings.ToLower(s) {
	case "", "0":
		return 0, nil
	case "permanent":
		return Permanent, nil
	}
	if strings.HasPrefix(s, "-") {
		return 0, fmt.Errorf("durationx: negative duration %q", s)
	}
	var total time.Duration
	rest := s
	// Consume a leading "<n>d" component, which time.ParseDuration does not support.
	if i := strings.IndexByte(rest, 'd'); i > 0 {
		n, err := strconv.ParseInt(rest[:i], 10, 64)
		if err == nil {
			if n > int64(maxDuration/day) {
				return 0, fmt.Errorf("durationx: duration %q overflows", s)
			}
			total = time.Duration(n) * day
			rest = rest[i+1:]
		}
	}
	if rest != "" {
		d, err := time.ParseDuration(rest)
		if err != nil {
			return 0, fmt.Errorf("durationx: invalid duration %q", s)
		}
		if d < 0 {
			return 0, fmt.Errorf("durationx: negative duration %q", s)
		}
		if total > maxDuration-d {
			return 0, fmt.Errorf("durationx: duration %q overflows", s)
		}
		total += d
	}
	return Duration(total), nil
}

// MustParse is Parse that panics on error; intended for constants and tests.
func MustParse(s string) Duration {
	d, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return d
}

// Std returns the value as time.Duration (-1ns for Permanent).
func (d Duration) Std() time.Duration { return time.Duration(d) }

// Milliseconds returns the duration in milliseconds (-1 for Permanent).
func (d Duration) Milliseconds() int64 {
	if d.IsPermanent() {
		return -1
	}
	return time.Duration(d).Milliseconds()
}

// IsPermanent reports whether d is Permanent.
func (d Duration) IsPermanent() bool { return d == Permanent }

// IsZero reports whether d is zero.
func (d Duration) IsZero() bool { return d == 0 }

// String returns the canonical representation, e.g. "30s", "10m", "7d",
// "1h30m", "1d12h", "250ms", "0s" or "permanent".
func (d Duration) String() string {
	if d.IsPermanent() {
		return "permanent"
	}
	td := time.Duration(d)
	if td == 0 {
		return "0s"
	}
	if td < 0 {
		return td.String()
	}
	var b strings.Builder
	if days := td / day; days > 0 {
		b.WriteString(strconv.FormatInt(int64(days), 10))
		b.WriteByte('d')
		td -= days * day
	}
	if h := td / time.Hour; h > 0 {
		b.WriteString(strconv.FormatInt(int64(h), 10))
		b.WriteByte('h')
		td -= h * time.Hour
	}
	if m := td / time.Minute; m > 0 {
		b.WriteString(strconv.FormatInt(int64(m), 10))
		b.WriteByte('m')
		td -= m * time.Minute
	}
	if s := td / time.Second; s > 0 {
		b.WriteString(strconv.FormatInt(int64(s), 10))
		b.WriteByte('s')
		td -= s * time.Second
	}
	if td > 0 {
		if td%time.Millisecond == 0 {
			b.WriteString(strconv.FormatInt(int64(td/time.Millisecond), 10))
			b.WriteString("ms")
		} else {
			b.WriteString(td.String())
		}
	}
	return b.String()
}

// MarshalJSON encodes the canonical string form.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}

// UnmarshalJSON accepts a duration string or an integer number of milliseconds.
func (d *Duration) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		v, err := Parse(s)
		if err != nil {
			return err
		}
		*d = v
		return nil
	}
	if string(data) == "null" {
		*d = 0
		return nil
	}
	var ms int64
	if err := json.Unmarshal(data, &ms); err != nil {
		return errors.New("durationx: duration must be a string or integer milliseconds")
	}
	v, err := fromMillis(ms)
	if err != nil {
		return err
	}
	*d = v
	return nil
}

// fromMillis converts an integer millisecond count (-1 = Permanent).
func fromMillis(ms int64) (Duration, error) {
	switch {
	case ms == -1:
		return Permanent, nil
	case ms < -1:
		return 0, fmt.Errorf("durationx: negative duration %d", ms)
	case ms > maxMillis:
		return 0, fmt.Errorf("durationx: duration %dms overflows", ms)
	default:
		return Duration(time.Duration(ms) * time.Millisecond), nil
	}
}

// MarshalYAML encodes the canonical string form.
func (d Duration) MarshalYAML() (any, error) {
	return d.String(), nil
}

// UnmarshalYAML accepts a duration string or an integer number of milliseconds.
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("durationx: line %d: duration must be a scalar", node.Line)
	}
	// ShortTag resolves implicit tags, so hand-built nodes without an explicit
	// Tag are classified the same way as parsed ones.
	if node.ShortTag() == "!!int" {
		ms, err := strconv.ParseInt(node.Value, 10, 64)
		if err != nil {
			return fmt.Errorf("durationx: line %d: invalid duration %q", node.Line, node.Value)
		}
		v, err := fromMillis(ms)
		if err != nil {
			return fmt.Errorf("line %d: %w", node.Line, err)
		}
		*d = v
		return nil
	}
	v, err := Parse(node.Value)
	if err != nil {
		return fmt.Errorf("line %d: %w", node.Line, err)
	}
	*d = v
	return nil
}
