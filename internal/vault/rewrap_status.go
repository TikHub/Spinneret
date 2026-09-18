package vault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/TikHub/Spinneret/internal/vault/vaultdb"
)

// KEKInfo describes one key-encryption key.
type KEKInfo struct {
	ID string
	// Current reports whether new DEKs are wrapped with this KEK.
	Current bool
	// Configured reports whether this instance holds the KEK; false means
	// records wrapped with it cannot be decrypted.
	Configured bool
	// WrappedRecords counts records whose DEK is wrapped with this KEK.
	WrappedRecords int64
}

// KEKStatus reports the KEK configuration and the rewrap progress.
type KEKStatus struct {
	CurrentKEKID string
	// KEKs lists configured KEKs and KEK ids still referenced by stored
	// records, sorted by ID.
	KEKs []KEKInfo
	// Running reports whether a rewrap job is running on any instance.
	Running bool
	// Done and Total count re-wrapped and pending records of the running or
	// last job.
	Done  int64
	Total int64
	// LastError is the error of the last job; empty when it succeeded.
	LastError      string
	LastFinishedAt *time.Time
}

// rewrapState is the JSON document stored under RewrapStatusKey.
type rewrapState struct {
	Running     bool       `json:"running"`
	Instance    string     `json:"instance,omitempty"`
	TargetKEKID string     `json:"target_kek_id,omitempty"`
	Done        int64      `json:"done"`
	Total       int64      `json:"total"`
	Failed      int64      `json:"failed"`
	LastError   string     `json:"last_error,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
	HeartbeatAt *time.Time `json:"heartbeat_at,omitempty"`
}

// errRewrapInterrupted is reported when the persisted status claims a running
// job whose instance stopped sending heartbeats.
const errRewrapInterrupted = "rewrap job stopped before finishing (instance lost); start it again"

// Status returns the KEK configuration, wrapped-record counts per KEK id and
// the progress of the running or last rewrap job.
func (r *Rewrapper) Status(ctx context.Context) (KEKStatus, error) {
	provider := r.cipher.Provider()
	if provider == nil {
		return KEKStatus{}, errNoProvider
	}
	ctx, cancel := context.WithTimeout(ctx, rewrapStatusTimeout)
	defer cancel()
	counts, err := r.recordCounts(ctx)
	if err != nil {
		return KEKStatus{}, err
	}
	state, err := r.loadState(ctx)
	if err != nil {
		return KEKStatus{}, err
	}
	if job := r.job.Load(); job != nil && r.running.Load() {
		// The local job is fresher than the persisted heartbeat.
		state = job.snapshot(r.now().UTC())
	}

	current := provider.CurrentID()
	configured := provider.IDs()
	status := KEKStatus{
		CurrentKEKID:   current,
		KEKs:           mergeKEKInfo(current, configured, counts),
		Done:           state.Done,
		Total:          state.Total,
		LastError:      state.LastError,
		LastFinishedAt: state.FinishedAt,
	}
	switch {
	case !state.Running:
	case r.running.Load():
		status.Running = true
	case state.HeartbeatAt != nil && r.now().Sub(*state.HeartbeatAt) < rewrapStaleAfter:
		status.Running = true
	default:
		if status.LastError == "" {
			status.LastError = errRewrapInterrupted
		}
	}
	return status, nil
}

// kekCountsTTL bounds how long per-KEK record counts are reused: counting
// scans every envelope table, and consoles poll the status during a rewrap.
const kekCountsTTL = 5 * time.Second

// kekCounts caches the result of VaultKEKRecordCounts.
type kekCounts struct {
	mu   sync.Mutex
	at   time.Time
	rows []vaultdb.VaultKEKRecordCountsRow
}

// recordCounts returns the wrapped-record counts per KEK id, from the cache
// when it is fresh.
func (r *Rewrapper) recordCounts(ctx context.Context) ([]vaultdb.VaultKEKRecordCountsRow, error) {
	now := r.now()
	r.counts.mu.Lock()
	if r.counts.rows != nil && now.Sub(r.counts.at) < kekCountsTTL {
		rows := slices.Clone(r.counts.rows)
		r.counts.mu.Unlock()
		return rows, nil
	}
	r.counts.mu.Unlock()

	rows, err := r.q.VaultKEKRecordCounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("vault: count wrapped records: %w", err)
	}
	r.counts.mu.Lock()
	r.counts.rows, r.counts.at = slices.Clone(rows), now
	r.counts.mu.Unlock()
	return rows, nil
}

// invalidateCounts drops cached record counts (after a job changed them).
func (r *Rewrapper) invalidateCounts() {
	r.counts.mu.Lock()
	r.counts.rows = nil
	r.counts.mu.Unlock()
}

func mergeKEKInfo(current string, configured []string, counts []vaultdb.VaultKEKRecordCountsRow) []KEKInfo {
	byID := make(map[string]*KEKInfo, len(configured)+len(counts))
	for _, id := range configured {
		byID[id] = &KEKInfo{ID: id, Configured: true, Current: id == current}
	}
	for _, c := range counts {
		info, ok := byID[c.KekID]
		if !ok {
			info = &KEKInfo{ID: c.KekID, Current: c.KekID == current}
			byID[c.KekID] = info
		}
		info.WrappedRecords = c.Records
	}
	out := make([]KEKInfo, 0, len(byID))
	for _, info := range byID {
		out = append(out, *info)
	}
	slices.SortFunc(out, func(a, b KEKInfo) int {
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		default:
			return 0
		}
	})
	return out
}

// loadState reads the persisted rewrap status; a missing row is an empty state.
func (r *Rewrapper) loadState(ctx context.Context) (rewrapState, error) {
	row, err := r.q.VaultSettingGet(ctx, RewrapStatusKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return rewrapState{}, nil
	}
	if err != nil {
		return rewrapState{}, fmt.Errorf("vault: load rewrap status: %w", err)
	}
	var st rewrapState
	decodeErr := json.Unmarshal(row.Value, &st)
	if decodeErr == nil {
		return st, nil
	}
	// A corrupted document must not break status reporting.
	r.logger.Warn("kek rewrap: invalid persisted status ignored", slog.Any("error", decodeErr))
	return rewrapState{}, nil
}

// persist stores the rewrap status document.
func (r *Rewrapper) persist(ctx context.Context, st rewrapState) error {
	value, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("vault: encode rewrap status: %w", err)
	}
	if err := r.q.VaultSettingPut(ctx, vaultdb.VaultSettingPutParams{Key: RewrapStatusKey, Value: value}); err != nil {
		return fmt.Errorf("vault: persist rewrap status: %w", err)
	}
	return nil
}
