//go:build e2e

package e2e

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// webhookDelivery is one notification received by the sink.
type webhookDelivery struct {
	ID        string         `json:"id"`
	Kind      string         `json:"kind"`
	Severity  string         `json:"severity"`
	Title     string         `json:"title"`
	Namespace string         `json:"namespace"`
	Site      string         `json:"site"`
	Details   map[string]any `json:"details"`
	CreatedAt time.Time      `json:"created_at"`
}

// webhookSink is an HTTP server receiving Spinneret webhook notifications. Every delivery must carry a
// valid X-Spinneret-Signature: "sha256=" + hex(HMAC-SHA256(secret, timestamp + "." + body)).
type webhookSink struct {
	secret string
	srv    *http.Server

	mu            sync.Mutex
	deliveries    []webhookDelivery
	badSignatures []string
	changed       chan struct{}
}

// startSink listens on addr until the test ends.
func startSink(t *testing.T, addr, secret string) *webhookSink {
	t.Helper()
	s := &webhookSink{secret: secret, changed: make(chan struct{})}
	mux := http.NewServeMux()
	mux.HandleFunc("/hook", s.handle)
	ln, err := net.Listen("tcp", addr)
	require.NoErrorf(t, err, "listen webhook sink on %s", addr)
	s.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = s.srv.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.srv.Shutdown(ctx)
	})
	return s
}

func (s *webhookSink) handle(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil || r.Method != http.MethodPost {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ts := r.Header.Get("X-Spinneret-Timestamp")
	sig := r.Header.Get("X-Spinneret-Signature")
	if err := verifySignature(s.secret, ts, sig, body, time.Now()); err != nil {
		s.mu.Lock()
		s.badSignatures = append(s.badSignatures, err.Error())
		s.mu.Unlock()
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	var d webhookDelivery
	if err := json.Unmarshal(body, &d); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.deliveries = append(s.deliveries, d)
	close(s.changed)
	s.changed = make(chan struct{})
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// verifySignature checks the webhook signature headers against the raw body.
func verifySignature(secret, timestamp, signature string, body []byte, now time.Time) error {
	if timestamp == "" || signature == "" {
		return errors.New("missing signature headers")
	}
	sec, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid timestamp %q", timestamp)
	}
	if d := now.Sub(time.Unix(sec, 0)); d > 5*time.Minute || d < -5*time.Minute {
		return fmt.Errorf("timestamp %s outside the 5 minute tolerance", timestamp)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte{'.'})
	mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(signature)) {
		return errors.New("signature mismatch")
	}
	return nil
}

// wait returns the first delivery (in arrival order) matching match within timeout.
func (s *webhookSink) wait(t *testing.T, timeout time.Duration, what string, match func(webhookDelivery) bool) webhookDelivery {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		s.mu.Lock()
		for _, d := range s.deliveries {
			if match(d) {
				s.mu.Unlock()
				return d
			}
		}
		changed := s.changed
		kinds := make([]string, 0, len(s.deliveries))
		for _, d := range s.deliveries {
			kinds = append(kinds, d.Kind)
		}
		s.mu.Unlock()
		select {
		case <-changed:
		case <-deadline.C:
			t.Fatalf("webhook %s not received within %s (received kinds: %v)", what, timeout, kinds)
		}
	}
}

// requireValidSignatures asserts that no delivery had an invalid signature.
func (s *webhookSink) requireValidSignatures(t *testing.T) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	require.Emptyf(t, s.badSignatures, "webhook deliveries with invalid signatures")
}

// sseEvent is one Server-Sent Event of the console event stream.
type sseEvent struct {
	Type string
	Data struct {
		Type   string          `json:"type"`
		SiteID string          `json:"site_id"`
		Data   json.RawMessage `json:"data"`
	}
}

// eventStream reads /api/v1/events/stream in the background.
type eventStream struct {
	cancel context.CancelFunc
	done   chan struct{}

	mu      sync.Mutex
	events  []sseEvent
	changed chan struct{}
}

// openEventStream connects with the console session (through the load balancer) and waits for the
// server's ": connected" comment.
func openEventStream(t *testing.T, c *console, namespace string) *eventStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	u := fmt.Sprintf("%s/api/v1/events/stream?tenant=%s&namespace=%s", c.baseURL, c.tenantID, namespace)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	require.NoError(t, err)
	hc := &http.Client{Jar: c.http.Jar} // no timeout: the stream is long-lived
	resp, err := hc.Do(req)
	if err != nil {
		cancel()
		require.NoError(t, err, "open event stream")
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		cancel()
		t.Fatalf("event stream status %d: %s", resp.StatusCode, b)
	}
	s := &eventStream{cancel: cancel, done: make(chan struct{}), changed: make(chan struct{})}
	connected := make(chan struct{})
	go func() {
		defer close(s.done)
		defer func() { _ = resp.Body.Close() }()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		var once sync.Once
		var cur sseEvent
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, ": connected"):
				once.Do(func() { close(connected) })
			case strings.HasPrefix(line, "event: "):
				cur.Type = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &cur.Data)
			case line == "" && cur.Type != "":
				s.mu.Lock()
				s.events = append(s.events, cur)
				close(s.changed)
				s.changed = make(chan struct{})
				s.mu.Unlock()
				cur = sseEvent{}
			}
		}
	}()
	select {
	case <-connected:
	case <-time.After(15 * time.Second):
		s.close()
		t.Fatal("event stream did not confirm the connection")
	}
	t.Cleanup(s.close)
	return s
}

func (s *eventStream) close() {
	s.cancel()
	<-s.done
}

// wait returns the first event matching match within timeout.
func (s *eventStream) wait(t *testing.T, timeout time.Duration, what string, match func(sseEvent) bool) sseEvent {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		s.mu.Lock()
		for _, ev := range s.events {
			if match(ev) {
				s.mu.Unlock()
				return ev
			}
		}
		changed := s.changed
		n := len(s.events)
		s.mu.Unlock()
		select {
		case <-changed:
		case <-s.done:
			t.Fatalf("event stream closed while waiting for %s", what)
		case <-deadline.C:
			t.Fatalf("event %s not received within %s (%d events received)", what, timeout, n)
		}
	}
}
