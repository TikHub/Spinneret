package proxy

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/TikHub/Spinneret/internal/apperr"
)

// Import formats.
const (
	FormatLines = "lines"
	FormatJSONL = "jsonl"
	FormatCSV   = "csv"
)

// Import limits.
const (
	// MaxImportRows bounds the number of data rows of one import.
	MaxImportRows = 100_000
	// MaxImportBytes bounds the size of the import data (32 MiB).
	MaxImportBytes = 32 << 20
	// maxImportFailures bounds the per-row failures returned by one import.
	maxImportFailures = 1000
)

// Import attribute keys shared by all formats.
const (
	keyURL             = "url"
	keyKind            = "kind"
	keyRegion          = "region"
	keyCity            = "city"
	keyProvider        = "provider"
	keyTags            = "tags"
	keyMaxConcurrency  = "max_concurrency"
	keySessionTemplate = "session_template"
)

// importRow is one parsed import row. Nil pointers mean "not set by the row".
type importRow struct {
	Line            int
	URL             string
	Kind            *string
	Region          *string
	City            *string
	Provider        *string
	Tags            []string
	TagsSet         bool
	MaxConcurrency  *int
	SessionTemplate *string
}

// ImportFailure describes one rejected import row. Messages never contain
// credentials.
type ImportFailure struct {
	Line    int
	Message string
}

// failureList collects row failures up to maxImportFailures entries and counts
// the rest.
type failureList struct {
	items   []ImportFailure
	dropped int
}

func (f *failureList) add(line int, format string, args ...any) {
	if len(f.items) >= maxImportFailures {
		f.dropped++
		return
	}
	f.items = append(f.items, ImportFailure{Line: line, Message: fmt.Sprintf(format, args...)})
}

func (f *failureList) count() int {
	return len(f.items) + f.dropped
}

// result returns the failures, with a trailing summary (line 0) when entries
// were dropped.
func (f *failureList) result() []ImportFailure {
	out := f.items
	if f.dropped > 0 {
		out = append(out, ImportFailure{Line: 0, Message: fmt.Sprintf("%d more rows failed", f.dropped)})
	}
	if out == nil {
		out = []ImportFailure{}
	}
	return out
}

// parseImport parses import data. Row-level problems are added to failures;
// the returned error (apperr InvalidArgument) rejects the whole import.
func parseImport(format, data string, failures *failureList) ([]importRow, error) {
	if len(data) > MaxImportBytes {
		return nil, apperr.InvalidArgument("", "import data exceeds %d bytes", MaxImportBytes)
	}
	switch format {
	case FormatLines:
		return parseLines(data, failures)
	case FormatJSONL:
		return parseJSONL(data, failures)
	case FormatCSV:
		return parseCSV(data, failures)
	default:
		return nil, apperr.InvalidArgument("", "format must be lines, jsonl or csv")
	}
}

// forEachLine calls fn with every line (without the line terminator) and its
// 1-based number.
func forEachLine(data string, fn func(line int, text string) error) error {
	line := 0
	for len(data) > 0 {
		line++
		text := data
		if i := strings.IndexByte(data, '\n'); i >= 0 {
			text, data = data[:i], data[i+1:]
		} else {
			data = ""
		}
		if err := fn(line, strings.TrimSuffix(text, "\r")); err != nil {
			return err
		}
	}
	return nil
}

func tooManyRows() error {
	return apperr.InvalidArgument("", "import exceeds %d rows", MaxImportRows)
}

// parseLines parses "url [key=value ...]" lines; blank lines and lines starting
// with "#" are ignored.
func parseLines(data string, failures *failureList) ([]importRow, error) {
	var rows []importRow
	err := forEachLine(data, func(line int, text string) error {
		text = strings.TrimSpace(text)
		if text == "" || strings.HasPrefix(text, "#") {
			return nil
		}
		if len(rows)+failures.count() >= MaxImportRows {
			return tooManyRows()
		}
		fields := strings.Fields(text)
		row := importRow{Line: line, URL: fields[0]}
		for i, kv := range fields[1:] {
			key, value, ok := strings.Cut(kv, "=")
			if !ok {
				failures.add(line, "field %d must be key=value", i+2)
				return nil
			}
			if err := row.set(strings.ToLower(key), value); err != nil {
				failures.add(line, "%v", err)
				return nil
			}
		}
		rows = append(rows, row)
		return nil
	})
	return rows, err
}

// set assigns one attribute from its textual form.
func (r *importRow) set(key, value string) error {
	switch key {
	case keyKind:
		r.Kind = &value
	case keyRegion:
		r.Region = &value
	case keyCity:
		r.City = &value
	case keyProvider:
		r.Provider = &value
	case keyTags:
		r.Tags, r.TagsSet = splitTags(value), true
	case keyMaxConcurrency:
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return errors.New("max_concurrency must be an integer")
		}
		r.MaxConcurrency = &n
	case keySessionTemplate:
		r.SessionTemplate = &value
	case keyURL:
		return errors.New("url must be the first field")
	default:
		return fmt.Errorf("unknown attribute %q", truncate(key, 32))
	}
	return nil
}

// jsonlRow is the JSON Lines row shape.
type jsonlRow struct {
	URL             *string   `json:"url"`
	Kind            *string   `json:"kind"`
	Region          *string   `json:"region"`
	City            *string   `json:"city"`
	Provider        *string   `json:"provider"`
	Tags            *[]string `json:"tags"`
	MaxConcurrency  *int      `json:"max_concurrency"`
	SessionTemplate *string   `json:"session_template"`
}

// parseJSONL parses one JSON object per non-blank line.
func parseJSONL(data string, failures *failureList) ([]importRow, error) {
	var rows []importRow
	err := forEachLine(data, func(line int, text string) error {
		if strings.TrimSpace(text) == "" {
			return nil
		}
		if len(rows)+failures.count() >= MaxImportRows {
			return tooManyRows()
		}
		var jr jsonlRow
		dec := json.NewDecoder(strings.NewReader(text))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&jr); err != nil {
			failures.add(line, "invalid JSON object: %s", jsonErrorHint(err))
			return nil
		}
		if dec.More() {
			failures.add(line, "invalid JSON object: unexpected data after the object")
			return nil
		}
		if jr.URL == nil {
			failures.add(line, "url is required")
			return nil
		}
		row := importRow{
			Line: line, URL: *jr.URL, Kind: jr.Kind, Region: jr.Region, City: jr.City,
			Provider: jr.Provider, MaxConcurrency: jr.MaxConcurrency, SessionTemplate: jr.SessionTemplate,
		}
		if jr.Tags != nil {
			row.Tags, row.TagsSet = *jr.Tags, true
		}
		rows = append(rows, row)
		return nil
	})
	return rows, err
}

// jsonErrorHint describes a JSON decoding error without echoing values, which
// may contain credentials.
func jsonErrorHint(err error) string {
	var typeErr *json.UnmarshalTypeError
	var syntaxErr *json.SyntaxError
	switch {
	case errors.As(err, &typeErr):
		return fmt.Sprintf("field %q has the wrong type", typeErr.Field)
	case errors.As(err, &syntaxErr):
		return fmt.Sprintf("syntax error at offset %d", syntaxErr.Offset)
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		return truncate(strings.TrimPrefix(err.Error(), "json: "), 64)
	case errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF):
		return "unexpected end of input"
	default:
		return "malformed object"
	}
}

// parseCSV parses a CSV document whose header names the columns. Record
// numbers are used as line numbers (header = 1).
func parseCSV(data string, failures *failureList) ([]importRow, error) {
	r := csv.NewReader(strings.NewReader(data))
	r.TrimLeadingSpace = true
	r.ReuseRecord = true
	header, err := r.Read()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, apperr.InvalidArgument("", "csv data is empty")
		}
		return nil, apperr.InvalidArgument("", "csv header is invalid")
	}
	columns, err := csvColumns(header)
	if err != nil {
		return nil, err
	}
	r.FieldsPerRecord = len(columns)
	var rows []importRow
	for record := 2; ; record++ {
		fields, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if len(rows)+failures.count() >= MaxImportRows {
			return nil, tooManyRows()
		}
		if err != nil {
			var perr *csv.ParseError
			if errors.As(err, &perr) && errors.Is(perr.Err, csv.ErrFieldCount) {
				failures.add(record, "expected %d fields", len(columns))
			} else {
				failures.add(record, "malformed csv record")
			}
			continue
		}
		row := importRow{Line: record}
		if rowErr := row.fromCSV(columns, fields); rowErr != nil {
			failures.add(record, "%v", rowErr)
			continue
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// csvColumns validates and normalizes the CSV header.
func csvColumns(header []string) ([]string, error) {
	columns := make([]string, len(header))
	seen := make(map[string]bool, len(header))
	for i, h := range header {
		name := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\uFEFF")))
		switch name {
		case keyURL, keyKind, keyRegion, keyCity, keyProvider, keyTags, keyMaxConcurrency, keySessionTemplate:
		default:
			return nil, apperr.InvalidArgument("", "csv header has unknown column %q", truncate(name, 32))
		}
		if seen[name] {
			return nil, apperr.InvalidArgument("", "csv header has duplicate column %q", name)
		}
		seen[name] = true
		columns[i] = name
	}
	if !seen[keyURL] {
		return nil, apperr.InvalidArgument("", "csv header must contain a url column")
	}
	return columns, nil
}

// fromCSV fills the row from a record; empty cells are "not set".
func (r *importRow) fromCSV(columns, fields []string) error {
	for i, name := range columns {
		value := strings.TrimSpace(fields[i])
		if name == keyURL {
			r.URL = value
			continue
		}
		if value == "" {
			continue
		}
		if err := r.set(name, value); err != nil {
			return err
		}
	}
	if r.URL == "" {
		return errors.New("url is required")
	}
	return nil
}
