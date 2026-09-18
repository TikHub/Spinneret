package action

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/Evil0ctal/Spinneret/internal/events"
)

// publishTimeout bounds one bus publish.
const publishTimeout = 2 * time.Second

// StateEventData is the Data payload of identity.state and proxy.state events
// published on the namespace channel. Until is null for permanent bans and
// changes without an end; SubjectKey is the hot-state hkey (set when the
// subject ID is unknown on the hot path, e.g. automatic account bans).
type StateEventData struct {
	SubjectKind string     `json:"subject_kind"`
	SubjectID   string     `json:"subject_id"`
	SubjectKey  int64      `json:"subject_key,omitempty"`
	SiteID      string     `json:"site_id"`
	From        string     `json:"from"`
	To          string     `json:"to"`
	Action      string     `json:"action"`
	Scope       string     `json:"scope,omitempty"`
	Until       *time.Time `json:"until"`
	Permanent   bool       `json:"permanent,omitempty"`
	Reason      string     `json:"reason"`
	Actor       string     `json:"actor,omitempty"`
}

// notifier publishes lifecycle changes on the event bus (best effort).
type notifier struct {
	bus    events.Bus
	logger *slog.Logger
}

// publish sends one state event for change c.
func (n notifier) publish(ctx context.Context, c StateChange) {
	if n.bus == nil || c.NamespaceID == "" || c.Shadow {
		return
	}
	typ := events.TypeIdentityState
	if c.SubjectKind == SubjectProxy {
		typ = events.TypeProxyState
	}
	data, err := json.Marshal(StateEventData{
		SubjectKind: c.SubjectKind,
		SubjectID:   c.SubjectID,
		SubjectKey:  keyIfUnknown(c),
		SiteID:      c.SiteID,
		From:        c.FromState,
		To:          c.ToState,
		Action:      c.Action,
		Scope:       c.Scope,
		Until:       c.Until,
		Permanent:   c.Permanent,
		Reason:      c.Reason,
		Actor:       c.Actor,
	})
	if err != nil {
		n.logger.Warn("encode state event", "error", err)
		return
	}
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), publishTimeout)
	defer cancel()
	ev := events.Event{
		Type:        typ,
		TenantID:    c.TenantID,
		NamespaceID: c.NamespaceID,
		SiteID:      c.SiteID,
		At:          c.At,
		Data:        data,
	}
	if err := n.bus.Publish(pctx, events.NamespaceChannel(c.NamespaceID), ev); err != nil {
		n.logger.Warn("publish state event", "error", err, "type", typ, "subject_kind", c.SubjectKind)
	}
}

// publishAll publishes the lifecycle changes among changes: state
// transitions and ban extensions (cooldowns, stat resets and cooldown reverts
// keep the state and are not published).
func (n notifier) publishAll(ctx context.Context, changes []StateChange) {
	for _, c := range changes {
		if c.FromState != c.ToState || c.Action == OpBan {
			n.publish(ctx, c)
		}
	}
}

func keyIfUnknown(c StateChange) int64 {
	if c.SubjectID == "" {
		return c.SubjectKey
	}
	return 0
}
