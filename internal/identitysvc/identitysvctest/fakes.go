// Package identitysvctest provides fakes and a PostgreSQL-backed environment
// for tests of identitysvc and its Connect handler.
package identitysvctest

import (
	"context"
	"encoding/json"
	"slices"
	"sync"

	"github.com/TikHub/Spinneret/internal/audit"
	"github.com/TikHub/Spinneret/internal/authz"
	"github.com/TikHub/Spinneret/internal/catalog"
	"github.com/TikHub/Spinneret/internal/events"
	"github.com/TikHub/Spinneret/internal/identitysvc"
)

// SyncCall records one HotSyncer call.
type SyncCall struct {
	Method string // identities | remove | accounts
	SiteID string
	IDs    []string
	Opts   identitysvc.SyncOptions
}

// Hot is a recording identitysvc.HotSyncer and identitysvc.HotReader.
type Hot struct {
	mu    sync.Mutex
	calls []SyncCall
	// Err is returned by every sync call when set.
	Err error
	// State is returned by IdentityHotState; StateErr fails it.
	State    identitysvc.HotState
	StateErr error
}

var (
	_ identitysvc.HotSyncer = (*Hot)(nil)
	_ identitysvc.HotReader = (*Hot)(nil)
)

// SyncIdentities implements identitysvc.HotSyncer.
func (h *Hot) SyncIdentities(_ context.Context, siteID string, ids []string, opts identitysvc.SyncOptions) error {
	return h.record(SyncCall{Method: "identities", SiteID: siteID, IDs: slices.Clone(ids), Opts: opts})
}

// RemoveIdentities implements identitysvc.HotSyncer.
func (h *Hot) RemoveIdentities(_ context.Context, siteID string, ids []string) error {
	return h.record(SyncCall{Method: "remove", SiteID: siteID, IDs: slices.Clone(ids)})
}

// SyncAccounts implements identitysvc.HotSyncer.
func (h *Hot) SyncAccounts(_ context.Context, siteID string, ids []string) error {
	return h.record(SyncCall{Method: "accounts", SiteID: siteID, IDs: slices.Clone(ids)})
}

// IdentityHotState implements identitysvc.HotReader.
func (h *Hot) IdentityHotState(context.Context, *catalog.Site, string) (identitysvc.HotState, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.State, h.StateErr
}

func (h *Hot) record(c SyncCall) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, c)
	return h.Err
}

// Calls returns the recorded sync calls.
func (h *Hot) Calls() []SyncCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.calls)
}

// Reset clears the recorded calls.
func (h *Hot) Reset() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = nil
}

// SyncedIDs returns every identity ID passed to SyncIdentities with opts.
func (h *Hot) SyncedIDs(opts identitysvc.SyncOptions) []string {
	var out []string
	for _, c := range h.Calls() {
		if c.Method == "identities" && c.Opts == opts {
			out = append(out, c.IDs...)
		}
	}
	return out
}

// OperateCall records one Operator call.
type OperateCall struct {
	Method      string // identities | account | revert
	Namespace   string
	IDs         []string
	AccountID   string
	Request     identitysvc.OperationRequest
	Revert      identitysvc.RevertRequest
	PrincipalID string
}

// Operator is a recording identitysvc.Operator. Every identity passed to
// OperateIdentities succeeds unless listed in FailIDs.
type Operator struct {
	mu    sync.Mutex
	calls []OperateCall
	// Err fails every call when set.
	Err error
	// FailIDs lists identity IDs reported as failed.
	FailIDs []string
	// RevertIDs is returned by RevertActions.
	RevertIDs []string
}

var _ identitysvc.Operator = (*Operator)(nil)

// OperateIdentities implements identitysvc.Operator.
func (o *Operator) OperateIdentities(_ context.Context, p *authz.Principal, ns *catalog.Namespace, ids []string, req identitysvc.OperationRequest) (identitysvc.BulkResult, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls = append(o.calls, OperateCall{Method: "identities", Namespace: ns.ID, IDs: slices.Clone(ids), Request: req, PrincipalID: p.ID})
	if o.Err != nil {
		return identitysvc.BulkResult{}, o.Err
	}
	res := identitysvc.BulkResult{Matched: len(ids)}
	for _, id := range ids {
		if slices.Contains(o.FailIDs, id) {
			res.Failed = append(res.Failed, identitysvc.BulkFailure{ID: id, Reason: "failed_precondition", Message: "fake failure"})
			continue
		}
		res.Succeeded++
	}
	return res, nil
}

// OperateAccount implements identitysvc.Operator.
func (o *Operator) OperateAccount(_ context.Context, p *authz.Principal, ns *catalog.Namespace, accountID string, req identitysvc.OperationRequest) (identitysvc.BulkResult, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls = append(o.calls, OperateCall{Method: "account", Namespace: ns.ID, AccountID: accountID, Request: req, PrincipalID: p.ID})
	if o.Err != nil {
		return identitysvc.BulkResult{}, o.Err
	}
	return identitysvc.BulkResult{Matched: 1, Succeeded: 1}, nil
}

// RevertActions implements identitysvc.Operator.
func (o *Operator) RevertActions(_ context.Context, p *authz.Principal, ns *catalog.Namespace, req identitysvc.RevertRequest) (identitysvc.BulkResult, []string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls = append(o.calls, OperateCall{Method: "revert", Namespace: ns.ID, Revert: req, PrincipalID: p.ID})
	if o.Err != nil {
		return identitysvc.BulkResult{}, nil, o.Err
	}
	return identitysvc.BulkResult{Matched: len(o.RevertIDs), Succeeded: len(o.RevertIDs)}, slices.Clone(o.RevertIDs), nil
}

// Calls returns the recorded calls.
func (o *Operator) Calls() []OperateCall {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.calls)
}

// Audit is a recording audit.Recorder.
type Audit struct {
	mu      sync.Mutex
	entries []audit.Entry
}

var _ audit.Recorder = (*Audit)(nil)

// Record implements audit.Recorder.
func (a *Audit) Record(_ context.Context, e audit.Entry) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, e)
}

// Entries returns the recorded entries.
func (a *Audit) Entries() []audit.Entry {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.entries)
}

// Actions returns the recorded audit actions in order.
func (a *Audit) Actions() []string {
	var out []string
	for _, e := range a.Entries() {
		out = append(out, e.Action)
	}
	return out
}

// Last returns the last entry with the given action and whether one exists.
func (a *Audit) Last(action string) (audit.Entry, bool) {
	entries := a.Entries()
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Action == action {
			return entries[i], true
		}
	}
	return audit.Entry{}, false
}

// Events records events published on a bus.
type Events struct {
	mu     sync.Mutex
	events []events.Event
}

// Subscribe starts recording every event of bus.
func (r *Events) Subscribe(bus events.Bus) (unsubscribe func()) {
	return bus.Subscribe(events.ChannelAll, func(_ context.Context, _ string, ev events.Event) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.events = append(r.events, ev)
	})
}

// All returns the recorded events.
func (r *Events) All() []events.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events)
}

// Data decodes the Data of ev into a generic map.
func Data(ev events.Event) map[string]any {
	var out map[string]any
	if err := json.Unmarshal(ev.Data, &out); err != nil {
		return nil
	}
	return out
}
