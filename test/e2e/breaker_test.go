//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	spinneretv1 "github.com/Evil0ctal/Spinneret/gen/go/spinneret/v1"
)

// breakerState returns the live breaker of an endpoint group.
func (f *fixture) breakerState(ctx context.Context, t *testing.T, groupID string) *spinneretv1.BreakerStatus {
	t.Helper()
	res, err := f.console.breakers.GetBreaker(ctx, connect.NewRequest(&spinneretv1.GetBreakerRequest{EndpointGroupId: groupID}))
	require.NoError(t, err, "GetBreaker")
	return res.Msg.GetBreaker()
}

// transitionTo reports whether an SSE event is a breaker transition of groupID to state.
func transitionTo(ev sseEvent, groupID, state string) bool {
	if ev.Type != "breaker.transition" {
		return false
	}
	var d struct {
		EndpointGroupID string `json:"endpoint_group_id"`
		To              string `json:"to"`
		ToState         string `json:"to_state"`
	}
	if json.Unmarshal(ev.Data.Data, &d) != nil {
		return false
	}
	return d.EndpointGroupID == groupID && (d.To == state || d.ToState == state)
}

// breakerLoad runs workers that acquire the search group, request the mock target and report until
// stop is closed. It counts acquire failures by reason.
type breakerLoad struct {
	f       *fixture
	reasons sync.Map // reason -> *atomic.Int64
	reports atomic.Int64
	probes  atomic.Int64
	errs    chan error
}

func (b *breakerLoad) count(reason string) {
	v, _ := b.reasons.LoadOrStore(reason, new(atomic.Int64))
	v.(*atomic.Int64).Add(1)
}

func (b *breakerLoad) countOf(reason string) int64 {
	v, ok := b.reasons.Load(reason)
	if !ok {
		return 0
	}
	return v.(*atomic.Int64).Load()
}

func (b *breakerLoad) run(ctx context.Context, workers int) {
	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; ctx.Err() == nil; i++ {
				uri := fmt.Sprintf("/site/search?q=breaker-%d-%d", w, i)
				res, err := b.f.node.leases.Acquire(ctx, connect.NewRequest(&spinneretv1.AcquireRequest{
					Site: b.f.site, Client: "web", Uri: uri, WaitMs: 500,
				}))
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					ae, ok := asAPIError(err)
					if !ok {
						select {
						case b.errs <- err:
						default:
						}
						return
					}
					b.count(ae.Reason)
					sleepCtx(ctx, min(max(ae.RetryAfter, 100*time.Millisecond), 500*time.Millisecond))
					continue
				}
				if res.Msg.GetLease().GetProbe() {
					b.probes.Add(1)
				}
				wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
				fetched := b.f.crawler.fetch(wctx, res.Msg, uri)
				_, err = b.f.node.reports.Report(wctx, connect.NewRequest(&spinneretv1.ReportRequest{Reports: []*spinneretv1.Report{
					fetched.report(res.Msg.GetLease().GetLeaseId(), uri, reportID(b.f.runID), true),
				}}))
				cancel()
				if err != nil {
					select {
					case b.errs <- err:
					default:
					}
					return
				}
				b.reports.Add(1)
			}
		}()
	}
	wg.Wait()
}

// scenarioBreaker (f): every search request is rate limited → the search breaker opens (ListBreakers,
// SSE breaker.transition, webhook), Acquire fails with circuit_open; once the target recovers, half-open
// probes close the breaker again.
func scenarioBreaker(ctx context.Context, t *testing.T, f *fixture) {
	search := f.group("web", "search")
	stream := openEventStream(t, f.console, f.namespace)
	require.Equal(t, "closed", f.breakerState(ctx, t, search.GetId()).GetState())

	f.mock.setRules(ctx, t, mockRule{Prefix: searchURIPrefix, Mode: "rate_limit"})
	load := &breakerLoad{f: f, errs: make(chan error, 8)}
	loadCtx, stopLoad := context.WithCancel(ctx)
	loadDone := make(chan struct{})
	go func() {
		defer close(loadDone)
		load.run(loadCtx, 6)
	}()
	stop := func() {
		stopLoad()
		<-loadDone
	}
	defer stop()

	openAt := time.Now()
	eventually(t, 45*time.Second, 250*time.Millisecond, "breaker open", func() (bool, string) {
		res, err := f.console.breakers.ListBreakers(ctx, connect.NewRequest(&spinneretv1.ListBreakersRequest{
			Namespace: f.namespace, Site: f.site, Client: "web", States: []string{"open"},
		}))
		if err != nil {
			return false, err.Error()
		}
		for _, b := range res.Msg.GetBreakers() {
			if b.GetEndpointGroupId() == search.GetId() {
				require.False(t, b.GetManual())
				require.GreaterOrEqual(t, b.GetWindow().GetRiskRatio(), 0.5)
				return true, ""
			}
		}
		return false, describe("%d reports sent, %d open breakers", load.reports.Load(), len(res.Msg.GetBreakers()))
	})
	// The target recovers right away: half-open probes after the open duration (5 s) must succeed.
	f.mock.clearRules(ctx, t)
	recoverAt := time.Now()
	t.Logf("breaker opened %s after the first rate-limited request (%d reports)", recoverAt.Sub(openAt).Round(time.Millisecond), load.reports.Load())

	// While open, Acquire is refused with unavailable/circuit_open.
	eventually(t, 5*time.Second, 25*time.Millisecond, "acquire refused with circuit_open", func() (bool, string) {
		_, err := f.node.leases.Acquire(ctx, connect.NewRequest(&spinneretv1.AcquireRequest{
			Site: f.site, Client: "web", Uri: "/site/search?q=open",
		}))
		if err == nil {
			return false, "acquire succeeded"
		}
		ae, _ := asAPIError(err)
		return ae.Code == connect.CodeUnavailable && ae.Reason == "circuit_open", err.Error()
	})
	stream.wait(t, 15*time.Second, "breaker.transition to open", func(ev sseEvent) bool {
		return ev.Data.SiteID == f.siteID && transitionTo(ev, search.GetId(), "open")
	})
	// The detail group has its own breaker and stays closed.
	require.Equal(t, "closed", f.breakerState(ctx, t, f.group("web", "detail").GetId()).GetState())

	eventually(t, 60*time.Second, 250*time.Millisecond, "breaker closed", func() (bool, string) {
		select {
		case err := <-load.errs:
			t.Fatalf("breaker load failed: %v", err)
		default:
		}
		b := f.breakerState(ctx, t, search.GetId())
		return b.GetState() == "closed", describe("state %s, probes %v, %d probe leases", b.GetState(), b.GetProbe(), load.probes.Load())
	})
	t.Logf("breaker closed %s after recovery; circuit_open refusals %d, probe leases %d",
		time.Since(recoverAt).Round(time.Millisecond), load.countOf("circuit_open"), load.probes.Load())
	stop()
	require.Positive(t, load.countOf("circuit_open"), "the load observed the open breaker")
	require.Positive(t, load.probes.Load(), "half-open issued probe leases")
	stream.wait(t, 15*time.Second, "breaker.transition to half_open", func(ev sseEvent) bool {
		return transitionTo(ev, search.GetId(), "half_open")
	})
	stream.wait(t, 15*time.Second, "breaker.transition to closed", func(ev sseEvent) bool {
		return transitionTo(ev, search.GetId(), "closed")
	})
	f.sink.wait(t, 60*time.Second, "breaker_opened", func(d webhookDelivery) bool {
		return d.Kind == "breaker_opened" && d.Details["endpoint_group_id"] == search.GetId()
	})
	f.sink.wait(t, 60*time.Second, "breaker_closed", func(d webhookDelivery) bool {
		return d.Kind == "breaker_closed" && d.Details["endpoint_group_id"] == search.GetId()
	})

	events, err := f.console.breakers.ListBreakerEvents(ctx, connect.NewRequest(&spinneretv1.ListBreakerEventsRequest{
		Namespace: f.namespace, Site: f.site, EndpointGroupId: search.GetId(), PageSize: 50,
	}))
	require.NoError(t, err, "ListBreakerEvents")
	var path []string
	for i := len(events.Msg.GetEvents()) - 1; i >= 0; i-- {
		ev := events.Msg.GetEvents()[i]
		path = append(path, ev.GetFromState()+">"+ev.GetToState()+"("+ev.GetTrigger()+")")
	}
	t.Logf("breaker transitions: %v", path)
	require.GreaterOrEqual(t, len(path), 3)
	require.Equal(t, "closed>open(auto)", path[0])
	require.Contains(t, path[len(path)-1], "half_open>closed")
	f.sink.requireValidSignatures(t)
}
