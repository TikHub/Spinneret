package signal

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEventJSONFieldNames(t *testing.T) {
	// The example of spec §6.2 must decode field by field.
	const example = `{"rid":"r-1","lid":"lse_x","ns":"ns_1","tn":"ten_1","tok":"tok_1","node":"crawler-hk-03","rcv":1758011411962,
 "uri":"/a/b","m":"GET","hs":200,"bc":"0","ek":"","mk":["empty_list"],"oh":"","lat":842,"rb":48213,
 "sa":1758011411120,"fa":1758011411962,"rel":true}`
	ev, err := DecodeEvent([]byte(example))
	require.NoError(t, err)
	require.Equal(t, Event{
		ReportID: "r-1", LeaseID: "lse_x", NamespaceID: "ns_1", TenantID: "ten_1", TokenID: "tok_1",
		Node: "crawler-hk-03", ReceivedAt: 1758011411962, URI: "/a/b", Method: "GET", HTTPStatus: 200,
		BusinessCode: "0", ErrorKind: "", Markers: []string{"empty_list"}, OutcomeHint: "", LatencyMs: 842,
		ResponseBytes: 48213, StartedAt: 1758011411120, FinishedAt: 1758011411962, Release: true,
	}, ev)

	b, err := EncodeEvent(ev)
	require.NoError(t, err)
	var got, want map[string]any
	require.NoError(t, json.Unmarshal(b, &got))
	require.NoError(t, json.Unmarshal([]byte(example), &want))
	require.Equal(t, want, got)
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		in   Event
	}{
		{name: "minimal", in: Event{ReportID: "r", LeaseID: "l", NamespaceID: "ns"}},
		{name: "full", in: Event{
			ReportID: "r2", LeaseID: "l2", NamespaceID: "ns", TenantID: "ten", TokenID: "tok", Node: "n",
			ReceivedAt: 5, URI: "/x?y=1", Method: "POST", HTTPStatus: 429, BusinessCode: "10001", ErrorKind: "timeout",
			Markers: []string{"a", "b"}, OutcomeHint: "captcha", LatencyMs: 1, ResponseBytes: 2, StartedAt: 3, FinishedAt: 4,
			Release: true,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := EncodeEvent(tt.in)
			require.NoError(t, err)
			out, err := DecodeEvent(b)
			require.NoError(t, err)
			want := tt.in
			if want.Markers == nil {
				want.Markers = []string{}
			}
			require.Equal(t, want, out)
		})
	}
}

func TestEncodeEventMarkersNeverNull(t *testing.T) {
	b, err := EncodeEvent(Event{ReportID: "r", LeaseID: "l", NamespaceID: "ns"})
	require.NoError(t, err)
	require.Contains(t, string(b), `"mk":[]`)
}

func TestEventValidation(t *testing.T) {
	tests := []struct {
		name string
		ev   Event
	}{
		{name: "missing rid", ev: Event{LeaseID: "l", NamespaceID: "ns"}},
		{name: "missing lid", ev: Event{ReportID: "r", NamespaceID: "ns"}},
		{name: "missing ns", ev: Event{ReportID: "r", LeaseID: "l"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := EncodeEvent(tt.ev)
			require.ErrorIs(t, err, ErrInvalidEvent)
			b, merr := json.Marshal(tt.ev)
			require.NoError(t, merr)
			_, err = DecodeEvent(b)
			require.ErrorIs(t, err, ErrInvalidEvent)
		})
	}
	_, err := DecodeEvent([]byte("{not json"))
	require.ErrorIs(t, err, ErrInvalidEvent)
	ev, err := DecodeEvent([]byte(`{"rid":"r","lid":"l","ns":"ns","future_field":1}`))
	require.NoError(t, err)
	require.Equal(t, []string{}, ev.Markers)
}

func TestEventTimes(t *testing.T) {
	ev := Event{ReceivedAt: 1758011411962, StartedAt: 0, FinishedAt: 1758011411000}
	require.Equal(t, time.UnixMilli(1758011411962), ev.Received())
	require.True(t, ev.Started().IsZero())
	require.Equal(t, time.UnixMilli(1758011411000), ev.Finished())
}
