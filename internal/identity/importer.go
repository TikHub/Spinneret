package identity

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
)

// Import formats accepted by ParseImport.
const (
	ImportFormatJSONL = "jsonl"
	ImportFormatCSV   = "csv"
)

// Default import limits (spec section 8).
const (
	DefaultImportMaxRows        = 50_000
	DefaultImportMaxBytes int64 = 32 << 20
)

// Reserved metadata keys of flat JSONL rows and CSV columns.
const (
	importAccountKey = "_account"
	importRegionKey  = "_region"
	importTagsKey    = "_tags"
	importLabelsKey  = "_labels"
)

// Per-row metadata limits. They bound the number of tags and labels on a
// single import row so that one row cannot force unbounded (previously
// O(n^2)) metadata processing regardless of the byte budget.
const (
	MaxTags   = 64
	MaxLabels = 64
)

// ImportRow is one parsed import row. Payload values are not normalized;
// pass them to CompiledType.Normalize.
type ImportRow struct {
	Line    int
	Payload map[string]any
	Account string
	Region  string
	Tags    []string
	Labels  map[string]string
}

// RowError describes a row that could not be parsed. Line is 1-based.
type RowError struct {
	Line    int
	Message string
}

// ImportLimits bounds an import. Zero or negative values select the defaults.
type ImportLimits struct {
	MaxRows  int
	MaxBytes int64
}

// errImportTooLarge is returned by the limited reader once MaxBytes is exceeded.
var errImportTooLarge = errors.New("import exceeds the byte limit")

// ParseImport parses identity import data.
//
// Format "jsonl": one JSON object per non-empty line. An object with a
// "payload" key is an envelope {payload, account, region, tags, labels}
// (tags: array of strings or comma-separated string; labels: object of
// strings). Otherwise the whole object is the payload, minus the reserved
// metadata keys _account, _region, _tags and _labels.
//
// Format "csv": a header row of field names is required. The reserved
// columns _account, _region, _tags (';'-separated) and _labels
// ("k=v;k2=v2") carry metadata. Empty cells are omitted and cells starting
// with '{' or '[' are decoded as JSON when valid; all other cells stay
// strings (Normalize coerces numbers, booleans and cookie header strings).
//
// A leading UTF-8 byte order mark is ignored and blank lines are skipped.
// Rows that cannot be parsed are reported as
// RowErrors with 1-based line numbers (the CSV header is line 1). Exceeding
// the limits, an unknown format, a read failure or an invalid CSV header is a
// fatal error.
func ParseImport(format string, r io.Reader, limits ImportLimits) ([]ImportRow, []RowError, error) {
	if r == nil {
		return nil, nil, invalidf("import: reader is nil")
	}
	if limits.MaxRows <= 0 {
		limits.MaxRows = DefaultImportMaxRows
	}
	if limits.MaxBytes <= 0 {
		limits.MaxBytes = DefaultImportMaxBytes
	}
	format = strings.ToLower(strings.TrimSpace(format))
	if format != ImportFormatJSONL && format != ImportFormatCSV {
		return nil, nil, invalidf("import: unsupported format %q (expected %q or %q)", truncate(format), ImportFormatJSONL, ImportFormatCSV)
	}
	br := bufio.NewReaderSize(&limitedReader{r: r, max: limits.MaxBytes}, importBufferSize)
	rows, rowErrs, err := parseImportData(format, br, limits)
	if err != nil {
		if errors.Is(err, errImportTooLarge) {
			return nil, nil, invalidf("import exceeds the limit of %d bytes", limits.MaxBytes)
		}
		return nil, nil, err
	}
	return rows, rowErrs, nil
}

// parseImportData skips a byte order mark and parses br in format, which
// ParseImport has already validated.
func parseImportData(format string, br *bufio.Reader, limits ImportLimits) ([]ImportRow, []RowError, error) {
	if err := skipBOM(br); err != nil {
		return nil, nil, err
	}
	if format == ImportFormatJSONL {
		return parseJSONL(br, limits)
	}
	return parseCSV(br, limits)
}

// importBufferSize is the read buffer size of ParseImport.
const importBufferSize = 64 << 10

var utf8BOM = []byte("\xef\xbb\xbf")

// skipBOM discards a leading UTF-8 byte order mark (spreadsheet exports often
// start with one), so that it can neither break a quoted CSV header nor end
// up in the first field name.
func skipBOM(br *bufio.Reader) error {
	prefix, err := br.Peek(len(utf8BOM))
	if bytes.Equal(prefix, utf8BOM) {
		_, err = br.Discard(len(utf8BOM))
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("import: read: %w", err)
	}
	return nil
}

// limitedReader fails with errImportTooLarge once more than max bytes have
// been read. The failure is sticky.
type limitedReader struct {
	r    io.Reader
	max  int64
	read int64
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.read > l.max {
		return 0, errImportTooLarge
	}
	// Allow one byte past the limit so that exceeding it is detectable,
	// without overflowing when max is math.MaxInt64.
	if remaining := l.max - l.read; remaining < int64(len(p)) {
		p = p[:remaining+1]
	}
	n, err := l.r.Read(p)
	l.read += int64(n)
	if l.read > l.max {
		return 0, errImportTooLarge
	}
	return n, err
}

func tooManyRows(limit int) error {
	return invalidf("import exceeds the limit of %d rows", limit)
}

// ---------------------------------------------------------------------------
// JSON Lines

func parseJSONL(br *bufio.Reader, limits ImportLimits) ([]ImportRow, []RowError, error) {
	var (
		rows    []ImportRow
		rowErrs []RowError
		count   int
	)
	for line := 1; ; line++ {
		data, readErr := br.ReadBytes('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, nil, fmt.Errorf("import: read line %d: %w", line, readErr)
		}
		if trimmed := bytes.TrimSpace(data); len(trimmed) > 0 {
			count++
			if count > limits.MaxRows {
				return nil, nil, tooManyRows(limits.MaxRows)
			}
			row, err := parseJSONLRow(trimmed)
			if err != nil {
				rowErrs = append(rowErrs, RowError{Line: line, Message: errorMessage(err)})
			} else {
				row.Line = line
				rows = append(rows, row)
			}
		}
		if errors.Is(readErr, io.EOF) {
			return rows, rowErrs, nil
		}
	}
}

func parseJSONLRow(data []byte) (ImportRow, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return ImportRow{}, fmt.Errorf("invalid JSON: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return ImportRow{}, errors.New("invalid JSON: unexpected data after the object")
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return ImportRow{}, errors.New("expected a JSON object")
	}
	if _, ok := obj["payload"]; ok {
		return parseEnvelopeRow(obj)
	}
	return parseFlatRow(obj)
}

var envelopeKeys = []string{"payload", "account", "region", "tags", "labels"}

func parseEnvelopeRow(obj map[string]any) (ImportRow, error) {
	for _, k := range sortedKeys(obj) {
		if !slices.Contains(envelopeKeys, k) {
			return ImportRow{}, fmt.Errorf("unknown envelope key %q (allowed: payload, account, region, tags, labels)", truncate(k))
		}
	}
	payload, ok := obj["payload"].(map[string]any)
	if !ok {
		return ImportRow{}, errors.New("payload must be a JSON object")
	}
	return buildJSONRow(payload, obj["account"], obj["region"], obj["tags"], obj["labels"], "")
}

func parseFlatRow(obj map[string]any) (ImportRow, error) {
	payload := make(map[string]any, len(obj))
	for k, v := range obj {
		switch k {
		case importAccountKey, importRegionKey, importTagsKey, importLabelsKey:
		default:
			payload[k] = v
		}
	}
	return buildJSONRow(payload, obj[importAccountKey], obj[importRegionKey], obj[importTagsKey], obj[importLabelsKey], "_")
}

// buildJSONRow assembles a row; keyPrefix is "_" for flat rows so messages
// name the key the user wrote.
func buildJSONRow(payload map[string]any, account, region, tags, labels any, keyPrefix string) (ImportRow, error) {
	if len(payload) == 0 {
		return ImportRow{}, errors.New("payload is empty")
	}
	row := ImportRow{Payload: payload}
	var err error
	if row.Account, err = optionalString(account, keyPrefix+"account"); err != nil {
		return ImportRow{}, err
	}
	if row.Region, err = optionalString(region, keyPrefix+"region"); err != nil {
		return ImportRow{}, err
	}
	if row.Tags, err = jsonTags(tags, keyPrefix+"tags"); err != nil {
		return ImportRow{}, err
	}
	if row.Labels, err = jsonLabels(labels, keyPrefix+"labels"); err != nil {
		return ImportRow{}, err
	}
	return row, nil
}

func optionalString(v any, key string) (string, error) {
	switch t := v.(type) {
	case nil:
		return "", nil
	case string:
		return strings.TrimSpace(t), nil
	default:
		return "", fmt.Errorf("%s must be a string", key)
	}
}

func jsonTags(v any, key string) ([]string, error) {
	switch t := v.(type) {
	case nil:
		return nil, nil
	case string:
		return splitList(t, ",", key)
	case []any:
		items := make([]string, 0, len(t))
		for _, item := range t {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("%s must be an array of strings or a comma-separated string", key)
			}
			items = append(items, s)
		}
		return cleanList(items, key)
	default:
		return nil, fmt.Errorf("%s must be an array of strings or a comma-separated string", key)
	}
}

func jsonLabels(v any, key string) (map[string]string, error) {
	switch t := v.(type) {
	case nil:
		return nil, nil
	case map[string]any:
		if len(t) == 0 {
			return nil, nil
		}
		if len(t) > MaxLabels {
			return nil, fmt.Errorf("%s has more than %d entries", key, MaxLabels)
		}
		out := make(map[string]string, len(t))
		for k, lv := range t {
			s, ok := lv.(string)
			if !ok {
				return nil, fmt.Errorf("%s.%s must be a string", key, truncate(k))
			}
			if strings.TrimSpace(k) == "" {
				return nil, fmt.Errorf("%s has an empty key", key)
			}
			out[k] = s
		}
		return out, nil
	default:
		return nil, fmt.Errorf("%s must be an object of strings", key)
	}
}

// splitList splits s on sep, trims items and drops empty and duplicate ones.
// key names the field for error messages. It rejects lists with more than
// MaxTags distinct values.
func splitList(s, sep, key string) ([]string, error) {
	return cleanList(strings.Split(s, sep), key)
}

// cleanList trims items, drops empty and duplicate ones in O(n) time, and
// rejects lists with more than MaxTags distinct values so that a single row's
// tag field cannot drive quadratic (or unbounded) work.
func cleanList(items []string, key string) ([]string, error) {
	capHint := min(len(items), MaxTags)
	out := make([]string, 0, capHint)
	seen := make(map[string]struct{}, capHint)
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, dup := seen[item]; dup {
			continue
		}
		if len(out) >= MaxTags {
			return nil, fmt.Errorf("%s has more than %d distinct values", key, MaxTags)
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// CSV

type csvColumn struct {
	name     string
	reserved bool
}

func parseCSV(r io.Reader, limits ImportLimits) ([]ImportRow, []RowError, error) {
	cr := csv.NewReader(r)
	cr.LazyQuotes = false
	cr.FieldsPerRecord = 0
	header, err := cr.Read()
	var headerErr *csv.ParseError
	switch {
	case errors.Is(err, io.EOF):
		return nil, nil, invalidf("import: csv header row is missing")
	case errors.Is(err, errImportTooLarge):
		return nil, nil, err
	case errors.As(err, &headerErr):
		return nil, nil, invalidf("import: invalid csv header: %v", headerErr)
	case err != nil:
		return nil, nil, fmt.Errorf("import: read csv header: %w", err)
	}
	columns, err := csvColumns(header)
	if err != nil {
		return nil, nil, err
	}
	var (
		rows    []ImportRow
		rowErrs []RowError
		count   int
	)
	for {
		record, err := cr.Read()
		if errors.Is(err, io.EOF) {
			return rows, rowErrs, nil
		}
		var pe *csv.ParseError
		switch {
		case errors.Is(err, errImportTooLarge):
			return nil, nil, err
		case errors.As(err, &pe):
			if errors.Is(pe.Err, csv.ErrFieldCount) && blankRecord(record) {
				continue
			}
			count++
			if count > limits.MaxRows {
				return nil, nil, tooManyRows(limits.MaxRows)
			}
			rowErrs = append(rowErrs, RowError{Line: pe.StartLine, Message: csvErrorMessage(pe)})
			continue
		case err != nil:
			return nil, nil, fmt.Errorf("import: read csv: %w", err)
		}
		if blankRecord(record) {
			continue
		}
		count++
		if count > limits.MaxRows {
			return nil, nil, tooManyRows(limits.MaxRows)
		}
		line, _ := cr.FieldPos(0)
		row, err := csvRow(columns, record)
		if err != nil {
			rowErrs = append(rowErrs, RowError{Line: line, Message: errorMessage(err)})
			continue
		}
		row.Line = line
		rows = append(rows, row)
	}
}

func csvErrorMessage(pe *csv.ParseError) string {
	if errors.Is(pe.Err, csv.ErrFieldCount) {
		return "wrong number of fields"
	}
	return fmt.Sprintf("invalid csv at line %d, column %d: %v", pe.Line, pe.Column, pe.Err)
}

func csvColumns(header []string) ([]csvColumn, error) {
	columns := make([]csvColumn, len(header))
	seen := make(map[string]struct{}, len(header))
	for i, name := range header {
		name = strings.TrimSpace(name)
		switch {
		case name == "":
			return nil, invalidf("import: csv header column %d is empty", i+1)
		case strings.HasPrefix(name, "_"):
			switch name {
			case importAccountKey, importRegionKey, importTagsKey, importLabelsKey:
			default:
				return nil, invalidf("import: csv header column %q is not a reserved column (allowed: _account, _region, _tags, _labels)", truncate(name))
			}
			columns[i] = csvColumn{name: name, reserved: true}
		default:
			columns[i] = csvColumn{name: name}
		}
		if _, dup := seen[name]; dup {
			return nil, invalidf("import: csv header column %q is duplicated", truncate(name))
		}
		seen[name] = struct{}{}
	}
	return columns, nil
}

func blankRecord(record []string) bool {
	for _, cell := range record {
		if strings.TrimSpace(cell) != "" {
			return false
		}
	}
	return true
}

func csvRow(columns []csvColumn, record []string) (ImportRow, error) {
	row := ImportRow{Payload: make(map[string]any, len(columns))}
	for i, col := range columns {
		cell := record[i]
		trimmed := strings.TrimSpace(cell)
		if trimmed == "" {
			continue
		}
		if !col.reserved {
			row.Payload[col.name] = csvCellValue(cell, trimmed)
			continue
		}
		switch col.name {
		case importAccountKey:
			row.Account = trimmed
		case importRegionKey:
			row.Region = trimmed
		case importTagsKey:
			tags, err := splitList(trimmed, ";", importTagsKey)
			if err != nil {
				return ImportRow{}, err
			}
			row.Tags = tags
		case importLabelsKey:
			labels, err := csvLabels(trimmed)
			if err != nil {
				return ImportRow{}, err
			}
			row.Labels = labels
		}
	}
	if len(row.Payload) == 0 {
		return ImportRow{}, errors.New("payload is empty")
	}
	return row, nil
}

// csvCellValue decodes JSON-looking cells and keeps everything else as the
// original string.
func csvCellValue(cell, trimmed string) any {
	if trimmed[0] != '{' && trimmed[0] != '[' {
		return cell
	}
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return cell
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return cell
	}
	return v
}

func csvLabels(s string) (map[string]string, error) {
	out := make(map[string]string)
	for part := range strings.SplitSeq(s, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return nil, errors.New("_labels must be k=v pairs separated by ';'")
		}
		if _, exists := out[k]; !exists && len(out) >= MaxLabels {
			return nil, fmt.Errorf("_labels has more than %d entries", MaxLabels)
		}
		out[k] = strings.TrimSpace(v)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}
