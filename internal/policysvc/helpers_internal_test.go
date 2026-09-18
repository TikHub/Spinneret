package policysvc

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/audit"
	"github.com/TikHub/Spinneret/internal/catalog/catalogtest"
	"github.com/TikHub/Spinneret/internal/events"
	"github.com/TikHub/Spinneret/internal/policy"
)

func TestValidateDebugReport(t *testing.T) {
	t.Parallel()
	long := func(n int) string { return strings.Repeat("a", n) }
	tests := []struct {
		name string
		r    DebugReport
		ok   bool
	}{
		{name: "empty", r: DebugReport{}, ok: true},
		{name: "full", r: DebugReport{URI: "/a", Method: "GET", HTTPStatus: 200, BusinessCode: "1", ErrorKind: "other", Markers: []string{"m"}, OutcomeHint: "success", LatencyMs: 1, ResponseBytes: 1}, ok: true},
		{name: "long uri", r: DebugReport{URI: "/" + long(3000)}},
		{name: "long method", r: DebugReport{Method: long(17)}},
		{name: "negative status", r: DebugReport{HTTPStatus: -1}},
		{name: "long business code", r: DebugReport{BusinessCode: long(65)}},
		{name: "too many markers", r: DebugReport{Markers: make([]string, 33)}},
		{name: "long marker", r: DebugReport{Markers: []string{long(65)}}},
		{name: "long hint", r: DebugReport{OutcomeHint: long(33)}},
		{name: "negative bytes", r: DebugReport{ResponseBytes: -1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDebugReport(tt.r)
			if tt.ok {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			_, isApp := apperr.As(err)
			require.True(t, isApp)
		})
	}
}

func TestDebugLimits(t *testing.T) {
	t.Parallel()
	counts := make(map[string]int64, maxDebugCounts+1)
	bans := make(map[string]int64, maxDebugBanCounts+1)
	for i := range maxDebugCounts + 1 {
		counts["identity:captcha:"+strconv.Itoa(i+1)+"s"] = 1
	}
	for i := range maxDebugBanCounts + 1 {
		bans[strconv.Itoa(i+1)+"d"] = 1
	}
	_, err := parseCounts(counts)
	require.Error(t, err)
	_, err = parseBanCounts(bans)
	require.Error(t, err)
	_, err = parseWindow("x", "", false)
	require.Error(t, err)
	d, err := parseWindow("x", "", true)
	require.NoError(t, err)
	require.Zero(t, d)
}

func TestSmallHelpers(t *testing.T) {
	t.Parallel()
	require.Equal(t, DefaultPageSize, normalizePageSize(0))
	require.Equal(t, MaxPageSize, normalizePageSize(10_000))
	require.Equal(t, 7, normalizePageSize(7))
	require.Equal(t, len(policy.Kinds()), kindOrder("bogus"))
	require.Equal(t, 4, levelOrder(policy.LevelBuiltin))
	require.Nil(t, identityTypes(policy.Default(policy.KindBreaker)))
	require.Empty(t, specDescription(nil))
	require.Nil(t, ptr(""))
	require.Equal(t, "x", deref(ptr("x")))
	require.Equal(t, policy.LevelSite, bindingLevel("s", "", ""))

	a := access{sites: map[string]struct{}{}}
	require.False(t, a.policyVisible([]string{""}))
	require.Empty(t, a.siteIDs())
	require.Nil(t, access{all: true}.siteIDs())

	// Cycles among other policies do not loop forever.
	depth, deepest := descendantDepth(map[string][]string{"root": {"a"}, "a": {"b"}, "b": {"a"}}, "root")
	require.Equal(t, 2, depth)
	require.Equal(t, "b", deepest)

	_, err := flattenYAML(policy.KindRotation, nil)
	require.Error(t, err)
	_, err = flattenYAML(policy.KindSignal, []policy.Spec{policy.Default(policy.KindAction)})
	require.Error(t, err)
	_, err = flattenYAML(policy.KindAction, []policy.Spec{policy.Default(policy.KindSignal)})
	require.Error(t, err)
	_, _, err = compileChain(policy.KindSignal, []policy.Spec{policy.Default(policy.KindAction)})
	require.Error(t, err)
	_, _, err = compileChain(policy.KindAction, []policy.Spec{policy.Default(policy.KindSignal)})
	require.Error(t, err)
	_, _, err = compileChain(policy.KindAction, nil)
	require.Error(t, err)
	_, _, err = compileChain(policy.KindSignal, nil)
	require.Error(t, err)
	sig, act, err := compileChain(policy.KindBreaker, nil)
	require.NoError(t, err)
	require.Nil(t, sig)
	require.Nil(t, act)

	require.Equal(t, []string{"boom"}, problems(errors.New("boom")))
}

// failingBus is an events.Bus whose Publish always fails.
type failingBus struct {
	mu    sync.Mutex
	calls int
}

func (b *failingBus) Publish(context.Context, string, events.Event) error {
	b.mu.Lock()
	b.calls++
	b.mu.Unlock()
	return errors.New("bus down")
}

func (b *failingBus) Subscribe(string, events.Handler) func() { return func() {} }

func (b *failingBus) Run(ctx context.Context) error { <-ctx.Done(); return nil }

func TestNewServiceDefaultsAndApply(t *testing.T) {
	t.Parallel()
	ns := catalogtest.NewNamespace("ten_1", "ns_1", "crawl")
	catalogtest.AddSite(ns, "sit_1", "a", 1)
	cat := catalogtest.New(ns)

	s := NewService(nil, cat, nil, nil, nil, nil)
	require.IsType(t, audit.Nop{}, s.audit)
	require.NotNil(t, s.logger)
	require.Nil(t, s.bus)

	// Without hot syncer and bus, apply only invalidates.
	s.apply(context.Background(), effects{ns: ns, kind: policy.KindRotation, syncAll: true})
	require.Equal(t, []string{"invalidate:ns_1"}, cat.Calls())

	// Bus failures are logged, not propagated; a namespace missing from the
	// catalog falls back to the stale snapshot's sites.
	bus := &failingBus{}
	s = NewService(nil, cat, nil, audit.Nop{}, slog.New(slog.NewTextHandler(io.Discard, nil)), WithEventBus(bus))
	cat.Remove("ns_1")
	s.apply(context.Background(), effects{ns: ns, kind: policy.KindAction, event: &PublishedEvent{Action: EventActionPublish}})
	require.Equal(t, 1, bus.calls)
	require.Equal(t, []string{"sit_1"}, s.sitesToSync(effects{ns: ns, syncAll: true}))
}
