// Package signal implements report ingestion (spec §6.2): it validates the
// reports sent by crawler nodes, authorizes them against the lease site,
// de-duplicates them by report ID and appends them to the report stream shard
// encoded in the lease ID. Classification and every state change happen later
// in the stream worker (internal/worker), which decodes the stream entries with
// DecodeEvent.
package signal

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Stream entry fields and version (spec §6.2): every entry is
// "v <StreamVersion> d <event JSON>".
const (
	StreamFieldVersion = "v"
	StreamFieldData    = "d"
	StreamVersion      = "1"
)

// ErrInvalidEvent is wrapped by EncodeEvent and DecodeEvent for events that
// lack a required field or cannot be parsed.
var ErrInvalidEvent = errors.New("signal: invalid report event")

// Event is one ingested report as carried by the report stream. The JSON field
// names are frozen by spec §6.2; EncodeEvent is the only producer.
type Event struct {
	// ReportID is the client-supplied idempotency key.
	ReportID string `json:"rid"`
	// LeaseID is the lease the request was made with.
	LeaseID string `json:"lid"`
	// NamespaceID and TenantID are those of the lease site.
	NamespaceID string `json:"ns"`
	TenantID    string `json:"tn"`
	// TokenID is the API token that sent the report ("" for other principals).
	TokenID string `json:"tok"`
	// Node is the sanitized X-Spinneret-Node header of the sender.
	Node string `json:"node"`
	// ReceivedAt is the ingest time in Unix milliseconds.
	ReceivedAt int64 `json:"rcv"`
	// URI and Method describe the request.
	URI    string `json:"uri"`
	Method string `json:"m"`
	// HTTPStatus is 0 when no response was received.
	HTTPStatus   int    `json:"hs"`
	BusinessCode string `json:"bc"`
	ErrorKind    string `json:"ek"`
	// Markers is never nil in encoded events.
	Markers       []string `json:"mk"`
	OutcomeHint   string   `json:"oh"`
	LatencyMs     int64    `json:"lat"`
	ResponseBytes int64    `json:"rb"`
	// StartedAt and FinishedAt are Unix milliseconds (0 when unknown).
	StartedAt  int64 `json:"sa"`
	FinishedAt int64 `json:"fa"`
	// Release asks the worker to release the lease after processing.
	Release bool `json:"rel"`
}

// Received returns ReceivedAt as a time.
func (e Event) Received() time.Time { return millisTime(e.ReceivedAt) }

// Started returns StartedAt as a time (zero when unknown).
func (e Event) Started() time.Time { return millisTime(e.StartedAt) }

// Finished returns FinishedAt as a time (zero when unknown).
func (e Event) Finished() time.Time { return millisTime(e.FinishedAt) }

func millisTime(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

// validate checks the fields every consumer relies on.
func (e Event) validate() error {
	switch {
	case e.ReportID == "":
		return fmt.Errorf("%w: rid is required", ErrInvalidEvent)
	case e.LeaseID == "":
		return fmt.Errorf("%w: lid is required", ErrInvalidEvent)
	case e.NamespaceID == "":
		return fmt.Errorf("%w: ns is required", ErrInvalidEvent)
	}
	return nil
}

// EncodeEvent serializes an event into the stream "d" field.
func EncodeEvent(e Event) ([]byte, error) {
	if err := e.validate(); err != nil {
		return nil, err
	}
	if e.Markers == nil {
		e.Markers = []string{}
	}
	b, err := json.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("encode report event: %w", err)
	}
	return b, nil
}

// DecodeEvent parses the stream "d" field. Unknown JSON fields are ignored so
// that newer producers stay readable.
func DecodeEvent(b []byte) (Event, error) {
	var e Event
	if err := json.Unmarshal(b, &e); err != nil {
		return Event{}, fmt.Errorf("%w: %w", ErrInvalidEvent, err)
	}
	if err := e.validate(); err != nil {
		return Event{}, err
	}
	if e.Markers == nil {
		e.Markers = []string{}
	}
	return e, nil
}
