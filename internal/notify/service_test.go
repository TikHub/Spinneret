package notify

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/TikHub/Spinneret/internal/events"
	"github.com/TikHub/Spinneret/internal/vault/vaulttest"
)

// trackingBus records whether Subscribe has been called yet, and takes its time
// doing it. The delay is what makes the test below a reliable detector rather
// than a second race: it holds Run inside Subscribe long enough that a readiness
// flag set before the call is certain to be observed while the handler is still
// missing. On a fast machine the two statements would otherwise run too close
// together to catch, which is why CI found this and a laptop did not.
type trackingBus struct {
	events.Bus
	subscribed atomic.Bool
}

func (b *trackingBus) Subscribe(channel string, h events.Handler) func() {
	time.Sleep(50 * time.Millisecond)
	b.subscribed.Store(true)
	return b.Bus.Subscribe(channel, h)
}

func TestSubscribedNeverLeadsTheSubscription(t *testing.T) {
	t.Parallel()
	// Everything that publishes an event and then waits for the alert waits on
	// Subscribed first. The bus hands an event to the handlers a channel holds at
	// the moment it is published and keeps no backlog, so a readiness signal that
	// arrives before the Subscribe call does not make the alert late — it loses it,
	// and no timeout afterwards can recover it.
	//
	// That is what `running` was: it is the guard against a second Run, so it is
	// set on Run's first statement, several statements before the handler exists.
	// Two CI builds failed on it in a row, each time in whichever bus test lost
	// the race, and raising the timeout to a minute only made the failure slower.
	e := newEnv(t, Config{})
	bus := &trackingBus{Bus: e.bus}
	svc := New(Config{RetryDelay: time.Millisecond}, e.pool, vaulttest.NewCipher(t), e.rdb, e.keys,
		e.cat, bus, e.audit, e.metrics, slog.New(slog.DiscardHandler))

	require.False(t, svc.Subscribed(), "before Run")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = svc.Run(ctx)
	}()

	require.Eventually(t, svc.Subscribed, alertWait, time.Millisecond)
	require.True(t, bus.subscribed.Load(), "Subscribed reported ready before the bus handler was registered")

	cancel()
	<-done
	require.False(t, svc.Subscribed(), "after Run returns")
}
