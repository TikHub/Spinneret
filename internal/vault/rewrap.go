package vault

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Evil0ctal/Spinneret/internal/jobs"
	"github.com/Evil0ctal/Spinneret/internal/vault/vaultdb"
)

// RewrapLockName is the PostgreSQL advisory lock (jobs.LockKey) held by the
// instance running the KEK rewrap job.
const RewrapLockName = "spinneret:kek:rewrap"

// RewrapStatusKey is the system_settings key holding the rewrap progress.
const RewrapStatusKey = "kek_rewrap_status"

const (
	rewrapBatchSize         = 500
	rewrapBatchTimeout      = 30 * time.Second
	rewrapCountTimeout      = 5 * time.Minute
	rewrapHeartbeatInterval = 2 * time.Second
	rewrapStaleAfter        = 30 * time.Second
	rewrapPersistTimeout    = 5 * time.Second
	rewrapStartTimeout      = 10 * time.Second
	rewrapStatusTimeout     = 30 * time.Second
	maxRewrapErrorLength    = 512
)

// ErrRewrapperClosed is returned by Rewrapper.Start after Close.
var ErrRewrapperClosed = errors.New("vault: rewrapper is closed")

// lockFactory acquires leader locks (implemented by jobs.PGLocks).
type lockFactory interface {
	TryLock(ctx context.Context, name string) (ok bool, lost <-chan struct{}, release func(), err error)
}

// Rewrapper re-wraps every DEK that is not wrapped by the current KEK, across
// identity_payloads, secret_versions, proxies, notification_channels and
// system_keys. Only ciphertext-independent wrapped DEKs change, so the job is
// cheap and safe to run while the system serves traffic. Exactly one instance
// runs the job at a time (PostgreSQL advisory lock RewrapLockName); progress is
// persisted in system_settings so every instance can report it.
type Rewrapper struct {
	q        *vaultdb.Queries
	cipher   *Cipher
	locks    lockFactory
	logger   *slog.Logger
	instance string
	now      func() time.Time

	batchSize      int
	heartbeatEvery time.Duration
	afterBatch     func(table string) // test hook, called after each batch

	base    context.Context
	stop    context.CancelFunc
	wg      sync.WaitGroup
	mu      sync.Mutex // serializes Start and Close
	closed  bool
	running atomic.Bool
	job     atomic.Pointer[rewrapJob]
	counts  kekCounts
}

// NewRewrapper returns a rewrapper. Background work started by Start stops
// when Close is called or when the context given to Run is canceled.
func NewRewrapper(pool *pgxpool.Pool, c *Cipher, logger *slog.Logger) *Rewrapper {
	if logger == nil {
		logger = slog.Default()
	}
	base, stop := context.WithCancel(context.Background())
	return &Rewrapper{
		q:              vaultdb.New(pool),
		cipher:         c,
		locks:          jobs.NewPGLocks(pool),
		logger:         logger.With(slog.String("component", "vault.rewrap")),
		instance:       randomInstanceID(),
		now:            time.Now,
		batchSize:      rewrapBatchSize,
		heartbeatEvery: rewrapHeartbeatInterval,
		base:           base,
		stop:           stop,
	}
}

func randomInstanceID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b[:])
}

// Start launches the rewrap job in the background. started is false (with a
// nil error) when a job is already running on this or another instance.
func (r *Rewrapper) Start(ctx context.Context) (started bool, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return false, ErrRewrapperClosed
	}
	if r.cipher.Provider() == nil {
		return false, errNoProvider
	}
	if !r.running.CompareAndSwap(false, true) {
		return false, nil
	}
	// Bounded so that a slow database cannot hold r.mu (and Close) for long.
	ctx, cancelStart := context.WithTimeout(ctx, rewrapStartTimeout)
	defer cancelStart()
	ok, lost, release, err := r.locks.TryLock(ctx, RewrapLockName)
	if err != nil {
		r.running.Store(false)
		return false, fmt.Errorf("vault: rewrap: acquire lock: %w", err)
	}
	if !ok {
		r.running.Store(false)
		return false, nil
	}

	now := r.now().UTC()
	job := newRewrapJob(r.instance, r.cipher.Provider().CurrentID(), now)
	if err := r.persist(ctx, job.snapshot(now)); err != nil {
		release()
		r.running.Store(false)
		return false, err
	}
	r.job.Store(job)
	jobCtx, cancel := context.WithCancel(r.base)
	r.wg.Add(2)
	go func() {
		defer r.wg.Done()
		select {
		case <-lost:
			r.logger.Warn("kek rewrap lock lost, stopping job")
			cancel()
		case <-jobCtx.Done():
		}
	}()
	go func() {
		defer r.wg.Done()
		defer r.running.Store(false)
		defer release()
		defer cancel()
		r.run(jobCtx, job)
	}()
	return true, nil
}

// Run blocks until ctx is canceled and then stops any running job (see Close).
// It lets the server manage the rewrapper like other long-running components.
func (r *Rewrapper) Run(ctx context.Context) error {
	<-ctx.Done()
	r.Close()
	return nil
}

// Close cancels a running job, waits for it to record its final status and
// makes further Start calls fail.
func (r *Rewrapper) Close() {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	r.stop()
	r.wg.Wait()
}

// Wait blocks until no job started by this instance is running or ctx ends.
func (r *Rewrapper) Wait(ctx context.Context) error {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for r.running.Load() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
	return nil
}

// run executes the job and records its outcome.
func (r *Rewrapper) run(ctx context.Context, job *rewrapJob) {
	r.logger.Info("kek rewrap started", slog.String("target_kek_id", job.target))
	hbCtx, stopHeartbeat := context.WithCancel(ctx)
	hbDone := make(chan struct{})
	go func() {
		defer close(hbDone)
		r.heartbeat(hbCtx, job)
	}()

	err := r.rewrapAll(ctx, job)

	stopHeartbeat()
	<-hbDone
	r.invalidateCounts()
	finished := r.now().UTC()
	job.finish(err, finished)
	pctx, cancel := context.WithTimeout(context.Background(), rewrapPersistTimeout)
	defer cancel()
	if perr := r.persist(pctx, job.snapshot(finished)); perr != nil {
		r.logger.Error("kek rewrap: persist final status failed", slog.Any("error", perr))
	}
	snap := job.snapshot(finished)
	attrs := []any{
		slog.String("target_kek_id", job.target), slog.Int64("done", snap.Done),
		slog.Int64("total", snap.Total), slog.Int64("failed", snap.Failed),
	}
	if snap.LastError != "" {
		r.logger.Error("kek rewrap finished with errors", append(attrs, slog.String("error", snap.LastError))...)
		return
	}
	r.logger.Info("kek rewrap finished", attrs...)
}

// heartbeat persists the job progress periodically until ctx is canceled.
func (r *Rewrapper) heartbeat(ctx context.Context, job *rewrapJob) {
	ticker := time.NewTicker(r.heartbeatEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pctx, cancel := context.WithTimeout(ctx, rewrapPersistTimeout)
			if err := r.persist(pctx, job.snapshot(r.now().UTC())); err != nil && ctx.Err() == nil {
				r.logger.Warn("kek rewrap: persist progress failed", slog.Any("error", err))
			}
			cancel()
		}
	}
}

// rewrapAll counts the pending records and processes every table in turn.
func (r *Rewrapper) rewrapAll(ctx context.Context, job *rewrapJob) error {
	cctx, cancel := context.WithTimeout(ctx, rewrapCountTimeout)
	total, err := r.q.VaultRewrapPendingCount(cctx, job.target)
	cancel()
	if err != nil {
		return fmt.Errorf("count records to re-wrap: %w", err)
	}
	job.setTotal(total)
	for _, t := range rewrapTables {
		if err := r.rewrapTable(ctx, t, job); err != nil {
			return fmt.Errorf("re-wrap %s: %w", t.name, err)
		}
	}
	// Records re-sealed under an old KEK while the job ran (an instance not
	// yet configured with the new current KEK) fail the compare-and-swap or
	// sit behind the keyset cursor. Report them so that an old KEK is never
	// retired while data still depends on it.
	cctx, cancel = context.WithTimeout(ctx, rewrapCountTimeout)
	remaining, err := r.q.VaultRewrapPendingCount(cctx, job.target)
	cancel()
	if err != nil {
		return fmt.Errorf("count records left on old keks: %w", err)
	}
	job.setRemaining(remaining)
	return nil
}

// rewrapTable walks the rows of one table that are not on the target KEK in
// keyset batches and re-wraps their DEKs with compare-and-swap updates.
func (r *Rewrapper) rewrapTable(ctx context.Context, t rewrapTableSpec, job *rewrapJob) error {
	var after rewrapRow
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		bctx, cancel := context.WithTimeout(ctx, rewrapBatchTimeout)
		rows, err := t.fetch(bctx, r.q, job.target, after, int32(r.batchSize))
		if err != nil {
			cancel()
			return fmt.Errorf("load batch: %w", err)
		}
		if len(rows) == 0 {
			cancel()
			return nil
		}
		batch := rewrapBatch{rows: make([]rewrapRow, 0, len(rows)), wrapped: make([][]byte, 0, len(rows))}
		for _, row := range rows {
			sealed, changed, err := r.cipher.Rewrap(Sealed{WrappedDEK: row.wrappedDEK, KEKID: row.kekID})
			if err != nil {
				job.recordFailure(fmt.Sprintf("%s %s: %v", t.name, row.ref(), err))
				continue
			}
			if !changed {
				job.addDone(1)
				continue
			}
			batch.newKEKID = sealed.KEKID
			batch.rows = append(batch.rows, row)
			batch.wrapped = append(batch.wrapped, sealed.WrappedDEK)
		}
		if len(batch.rows) > 0 {
			// Rows changed concurrently (new version sealed, row deleted) fail
			// the compare-and-swap; they no longer need re-wrapping.
			if _, err := t.update(bctx, r.q, batch); err != nil {
				cancel()
				return fmt.Errorf("update batch: %w", err)
			}
			job.addDone(int64(len(batch.rows)))
		}
		cancel()
		after = rows[len(rows)-1]
		if r.afterBatch != nil {
			r.afterBatch(t.name)
		}
		if len(rows) < r.batchSize {
			return nil
		}
	}
}

// rewrapJob tracks the progress of one job. It is safe for concurrent use.
type rewrapJob struct {
	instance string
	target   string
	started  time.Time

	mu        sync.Mutex
	done      int64
	total     int64
	failed    int64
	remaining int64 // records not on the target KEK after the walk
	firstFail string
	finished  *time.Time
	lastError string
}

func newRewrapJob(instance, target string, started time.Time) *rewrapJob {
	return &rewrapJob{instance: instance, target: target, started: started}
}

func (j *rewrapJob) setTotal(n int64) {
	j.mu.Lock()
	j.total = n
	j.mu.Unlock()
}

func (j *rewrapJob) setRemaining(n int64) {
	j.mu.Lock()
	j.remaining = n
	j.mu.Unlock()
}

func (j *rewrapJob) addDone(n int64) {
	j.mu.Lock()
	j.done += n
	j.mu.Unlock()
}

func (j *rewrapJob) recordFailure(msg string) {
	j.mu.Lock()
	j.failed++
	if j.firstFail == "" {
		j.firstFail = msg
	}
	j.mu.Unlock()
}

// finish records the outcome: err is a job-level failure; per-record
// failures are summarized as well.
func (j *rewrapJob) finish(err error, at time.Time) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.finished = &at
	var msg string
	if err != nil {
		msg = err.Error()
	}
	appendMsg := func(part string) {
		if msg != "" {
			msg += "; "
		}
		msg += part
	}
	if j.failed > 0 {
		appendMsg(fmt.Sprintf("%d records could not be re-wrapped (first: %s)", j.failed, j.firstFail))
	}
	if err == nil && j.remaining > 0 {
		appendMsg(fmt.Sprintf("%d records are still wrapped by a non-current KEK; start the rewrap again", j.remaining))
	}
	j.lastError = truncateUTF8(msg, maxRewrapErrorLength)
}

func (j *rewrapJob) snapshot(now time.Time) rewrapState {
	j.mu.Lock()
	defer j.mu.Unlock()
	started := j.started
	heartbeat := now
	return rewrapState{
		Running:     j.finished == nil,
		Instance:    j.instance,
		TargetKEKID: j.target,
		Done:        j.done,
		// Records sealed under an old KEK while the job runs are processed
		// too, so the initial count can be exceeded.
		Total:       max(j.total, j.done+j.failed),
		Failed:      j.failed,
		LastError:   j.lastError,
		StartedAt:   &started,
		FinishedAt:  j.finished,
		HeartbeatAt: &heartbeat,
	}
}

func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut]
}
