package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/TikHub/Spinneret/internal/apperr"
	"github.com/TikHub/Spinneret/internal/audit"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/notify/notifydb"
)

// Delivery bookkeeping limits.
const (
	persistTimeout   = 5 * time.Second
	testSendTimeout  = 15 * time.Second
	maxStatusBytes   = 512
	queueFullMessage = "delivery queue full"
	statusOK         = "ok"
)

// deliveryJob is one queued delivery of an alert through a channel.
type deliveryJob struct {
	alertID string
	msg     message
	target  target
}

// enqueue schedules a delivery without blocking; when the queue is full the
// delivery is recorded as failed.
func (s *Service) enqueue(job deliveryJob) {
	select {
	case s.queue <- job:
	default:
		n := s.droppedDeliveries.Add(1)
		if n%100 == 1 {
			s.logger.Error("notify delivery queue full, dropping deliveries", slog.Int64("dropped_total", n))
		}
		s.countDelivery(job.target.kind, resultDropped)
		s.recordDelivery(context.Background(), job, Delivery{
			ChannelID: job.target.id, ChannelName: job.target.name, OK: false,
			Error: queueFullMessage, Attempts: 0, At: s.now().UTC(),
		})
	}
}

// deliveryWorker processes queued deliveries until ctx is canceled. Channels
// whose previous delivery failed recently get a single attempt, so a dead
// endpoint cannot occupy the shared workers with retries and delay the
// deliveries of healthy channels.
func (s *Service) deliveryWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-s.queue:
			attempts := s.cfg.Attempts
			if s.failing.failing(job.target.id, s.now()) {
				attempts = 1
			}
			d := s.deliver(ctx, job.target, job.msg, attempts)
			if ctx.Err() != nil && !d.OK {
				// Shutdown interrupted the delivery; do not record a spurious failure.
				return
			}
			s.failing.observe(job.target.id, d.OK, s.now())
			s.recordDelivery(ctx, job, d)
		}
	}
}

// Failing channel tracking limits.
const (
	// failingWindow is how long a channel whose delivery failed is attempted
	// only once per delivery.
	failingWindow = 5 * time.Minute
	// maxFailingChannels bounds the tracked channels.
	maxFailingChannels = 10_000
)

// failingChannels remembers channels whose last delivery failed.
type failingChannels struct {
	mu    sync.Mutex
	until map[string]time.Time
}

func newFailingChannels() *failingChannels {
	return &failingChannels{until: map[string]time.Time{}}
}

// failing reports whether the last delivery of the channel failed within
// failingWindow.
func (f *failingChannels) failing(channelID string, now time.Time) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	until, ok := f.until[channelID]
	if ok && !now.Before(until) {
		delete(f.until, channelID)
		return false
	}
	return ok
}

// observe records the outcome of a delivery through the channel.
func (f *failingChannels) observe(channelID string, ok bool, now time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ok {
		delete(f.until, channelID)
		return
	}
	if len(f.until) >= maxFailingChannels {
		for id, until := range f.until {
			if !now.Before(until) {
				delete(f.until, id)
			}
		}
		if len(f.until) >= maxFailingChannels {
			f.until = map[string]time.Time{}
		}
	}
	f.until[channelID] = now.Add(failingWindow)
}

// deliver sends a message with up to attempts tries and exponential backoff.
func (s *Service) deliver(ctx context.Context, t target, msg message, attempts int) Delivery {
	d := Delivery{ChannelID: t.id, ChannelName: t.name}
	var err error
	for attempt := 1; attempt <= attempts; attempt++ {
		d.Attempts = attempt
		if t.cfgErr != nil {
			err = t.cfgErr
		} else {
			err = s.sender.send(ctx, t.kind, t.cfg, msg)
		}
		if err == nil || isPermanent(err) || ctx.Err() != nil || attempt == attempts {
			break
		}
		if !sleepCtx(ctx, s.cfg.RetryDelay<<(attempt-1)) {
			err = ctx.Err()
			break
		}
	}
	d.At = s.now().UTC()
	if err != nil {
		d.Error = errorSummary(err)
		s.countDelivery(t.kind, resultError)
		s.logger.Warn("alert delivery failed", slog.String("alert_id", msg.ID), slog.String("channel_id", t.id),
			slog.String("kind", t.kind), slog.Int("attempts", d.Attempts), slog.String("error", d.Error))
		return d
	}
	d.OK = true
	s.countDelivery(t.kind, resultOK)
	return d
}

// recordDelivery appends the delivery to the alert and updates the channel
// status. It runs detached from cancellation with a bounded timeout.
func (s *Service) recordDelivery(ctx context.Context, job deliveryJob, d Delivery) {
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
	defer cancel()
	if err := s.persistDelivery(pctx, job.alertID, d); err != nil {
		s.logger.Error("record alert delivery failed", slog.String("alert_id", job.alertID),
			slog.String("channel_id", d.ChannelID), slog.Any("error", err))
	}
}

func (s *Service) persistDelivery(ctx context.Context, alertID string, d Delivery) error {
	b, err := json.Marshal(d)
	if err != nil {
		return fmt.Errorf("encode delivery: %w", err)
	}
	if _, err := s.q.NotifyAlertAppendDelivery(ctx, notifydb.NotifyAlertAppendDeliveryParams{Delivery: b, ID: alertID}); err != nil {
		return fmt.Errorf("append delivery: %w", err)
	}
	status := statusOK
	if !d.OK {
		status = truncateBytes(d.Error, maxStatusBytes)
	}
	at := d.At
	if err := s.q.NotifyChannelSetDelivery(ctx, notifydb.NotifyChannelSetDeliveryParams{
		DeliveredAt: &at, Status: status, ID: d.ChannelID,
	}); err != nil {
		return fmt.Errorf("update channel delivery status: %w", err)
	}
	return nil
}

// TestChannel stores a "test" alert for the channel's tenant and namespace
// and delivers it synchronously through the channel (one attempt, ignoring the
// channel's filters and enabled flag).
func (s *Service) TestChannel(ctx context.Context, p *authz.Principal, id string) (Delivery, error) {
	row, err := s.q.NotifyChannelGet(ctx, id)
	if err != nil {
		return Delivery{}, mapChannelError(err, id)
	}
	ev, err := s.insertAlert(ctx, Alert{
		Kind:        KindTest,
		Severity:    SeverityInfo,
		TenantID:    row.TenantID,
		NamespaceID: deref(row.NamespaceID),
		Title:       "Test notification",
		Message:     fmt.Sprintf("This is a test notification for channel %q sent by %s.", row.Name, p.Actor()),
		Details:     map[string]any{"channel_id": row.ID, "channel": row.Name, "channel_kind": row.Kind},
	})
	if err != nil {
		return Delivery{}, apperr.Internal(err)
	}
	sctx, cancel := context.WithTimeout(ctx, testSendTimeout)
	defer cancel()
	d := s.deliver(sctx, s.targetOf(row), s.messageOf(ctx, ev), 1)
	if sctx.Err() == nil || d.OK {
		s.failing.observe(row.ID, d.OK, s.now())
	}
	s.recordDelivery(ctx, deliveryJob{alertID: ev.ID}, d)
	result := audit.ResultOK
	if !d.OK {
		result = audit.ResultError
	}
	entry := audit.FromPrincipal(p, row.TenantID, deref(row.NamespaceID), AuditChannelTest, AuditResourceKind,
		row.ID, row.Name, result, map[string]any{"alert_id": ev.ID, "ok": d.OK, "error": d.Error})
	s.audit.Record(ctx, entry)
	return d, nil
}

// sleepCtx waits for d or until ctx is done; it reports whether d elapsed.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
