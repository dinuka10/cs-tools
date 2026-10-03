// Copyright (c) 2026, WSO2 LLC. (https://www.wso2.com).
//
// WSO2 LLC. licenses this file to you under the Apache License,
// Version 2.0 (the "License"); you may not use this file except
// in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

// Package allocator assigns alert ids and writes the rows alerts-core reads.
//
// Claiming one id per alert makes every request compete for the single alert_seq row, which
// turns a burst into mostly failed compare-and-sets. Instead, handlers queue their alerts and
// one claimer goroutine per replica takes everything waiting (up to MaxBatch), claims the
// whole range N+1..N+n with a single compare-and-set, and hands the range to parallel writers
// without waiting for them. Replicas only contend on the claim itself, which takes
// milliseconds; they never wait on each other's inserts.
//
// alerts-core reads ids in order and waits gap_timeout on a missing id. So once an id is
// claimed it must get a row: writes use a context detached from the HTTP request, throttled
// writes are retried until store.write_deadline, and an alert that still can't be written gets
// a filler row alerts-core can't parse and skips immediately. The claimer only claims as many
// ids as there are free writer slots, and accepted-but-unfinished work is capped in bytes.
package allocator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"sre-alert-ingestion-service/internal/cassandra"
	"sre-alert-ingestion-service/internal/model"
)

// Errors returned by Submit. All of them map to 503: none of them leaves a claimed id empty
// through a fault of the request itself.
var (
	ErrQueueFull      = errors.New("alert queue full")
	ErrQueueBytesFull = errors.New("alert queue memory limit reached")
	ErrShuttingDown   = errors.New("shutting down")
	ErrClaimFailed    = errors.New("could not claim alert ids")
	ErrStoreFailed    = errors.New("alert could not be stored")
	ErrTimeout        = errors.New("timed out waiting for storage")
)

// fillerPrefix marks a row alerts-core can't parse as an alert: it logs
// "alert unprocessable, skipping" and moves to the next id without waiting.
const fillerPrefix = "VOID: "

// throttleJitter bounds the random extra wait added to Cosmos DB's RetryAfter. A var so tests
// can shorten it.
var throttleJitter = 100 * time.Millisecond

// fillerGrace is how long past the write deadline a throttled filler keeps retrying.
const fillerGrace = time.Minute

// memLogEvery rate-limits the "queue memory limit reached" warning.
const memLogEvery = time.Minute

// Store is the storage the allocator needs; *cassandra.Store implements it.
type Store interface {
	ReadSeq(ctx context.Context) (int64, error)
	// CompareAndSet moves alert_seq from `from` to `to`; when rejected, current is the value
	// the row actually holds.
	CompareAndSet(ctx context.Context, from, to int64) (applied bool, current int64, err error)
	Insert(ctx context.Context, id, vendor, alert string) error
	Exists(ctx context.Context, id string) (bool, error)
	// InsertFiller writes the filler only if id has no row; otherwise existing is that row's alert.
	InsertFiller(ctx context.Context, id, vendor, filler string) (applied bool, existing string, err error)
}

// StoreFailure describes an alert that couldn't be written after every attempt.
type StoreFailure struct {
	Vendor        string
	RequestID     string
	AltID         string
	Alert         model.Alert
	Err           error
	FillerWritten bool
}

// FailureNotifier is told about every alert that couldn't be stored (the DB-failure Chat card).
type FailureNotifier interface {
	StoreFailed(StoreFailure)
}

// Waker wakes alerts-core once per written batch. Implementations must not block.
type Waker interface {
	Wake()
}

// Config tunes the allocator; see config.toml.example.
type Config struct {
	QueueSize int
	// QueueMaxBytes caps the size of everything accepted but not yet finished.
	QueueMaxBytes    int64
	MaxBatch         int
	WriteConcurrency int
	ClaimMaxAttempts int
	InsertAttempts   int
	InsertBaseDelay  time.Duration
	// QueryTimeout is store.query_timeout; no insert starts with less than twice this left.
	QueryTimeout time.Duration
	// WriteDeadline, from claim time, bounds retries of throttled writes.
	WriteDeadline time.Duration
	// ClaimJitter bounds the random pause before retrying a rejected compare-and-set, so two
	// replicas that collided don't collide again in lockstep.
	ClaimJitter time.Duration
	// ReadBack confirms each insert with a read before counting the alert stored.
	ReadBack bool
}

// Result is what a submitter receives: its ids, in submission order, or an error.
type Result struct {
	IDs []string
	Err error
}

type submission struct {
	vendor    string
	requestID string
	alerts    []model.Alert
	size      int64
	released  atomic.Bool
	done      chan Result // buffered(1): the writer never blocks on a submitter that gave up
}

// Allocator is safe for concurrent Submit calls.
type Allocator struct {
	logger   *slog.Logger
	store    Store
	notifier FailureNotifier
	waker    Waker
	cfg      Config

	mu          sync.RWMutex // guards closed against the close of queue
	closed      bool
	queue       chan *submission
	writeSem    chan struct{} // one token per busy writer; the claimer reserves them
	writes      sync.WaitGroup
	claimerDone chan struct{}

	bytes      atomic.Int64 // size of accepted, unfinished submissions
	memRejects atomic.Int64
	memLogAt   atomic.Int64 // unix nanos of the last memory-limit warning

	pendingMu sync.Mutex
	pending   map[int64]struct{} // claimed ids whose row or filler isn't written yet

	// Shutdown drain: once Close starts with a deadline, retries stop at stopAt and each
	// unwritten id gets one filler attempt before the process exits.
	drainCh   chan struct{}
	drainOnce sync.Once
	stopAt    atomic.Int64 // unix nanos; 0 until a drain deadline is set

	// lastSeq is alert_seq as of this replica's last claim; only the claimer touches it. Valid
	// while seqKnown, so a claim skips reading alert_seq first.
	lastSeq  int64
	seqKnown bool
}

// New starts the claimer goroutine. notifier and waker may be nil.
func New(logger *slog.Logger, store Store, notifier FailureNotifier, waker Waker, cfg Config) *Allocator {
	a := &Allocator{
		logger:      logger,
		store:       store,
		notifier:    notifier,
		waker:       waker,
		cfg:         cfg,
		queue:       make(chan *submission, cfg.QueueSize),
		writeSem:    make(chan struct{}, cfg.WriteConcurrency),
		claimerDone: make(chan struct{}),
		pending:     map[int64]struct{}{},
		drainCh:     make(chan struct{}),
	}
	go a.claimLoop()
	return a
}

// Submit queues alerts and waits for their ids. A full queue fails immediately. If ctx ends
// first Submit returns ErrTimeout, but the alerts are still written: their ids may already be
// claimed, and a claimed id must never be left empty.
func (a *Allocator) Submit(ctx context.Context, vendor, requestID string, alerts []model.Alert) ([]string, error) {
	if len(alerts) == 0 {
		return []string{}, nil
	}
	size := sizeOf(alerts)
	if !a.reserveBytes(size) {
		a.noteMemReject()
		return nil, ErrQueueBytesFull
	}
	sub := &submission{vendor: vendor, requestID: requestID, alerts: alerts, size: size, done: make(chan Result, 1)}

	a.mu.RLock()
	if a.closed {
		a.mu.RUnlock()
		a.releaseBytes(sub)
		return nil, ErrShuttingDown
	}
	select {
	case a.queue <- sub:
		a.mu.RUnlock()
	default:
		a.mu.RUnlock()
		a.releaseBytes(sub)
		return nil, ErrQueueFull
	}

	select {
	case res := <-sub.done:
		return res.IDs, res.Err
	case <-ctx.Done():
		a.logger.Warn("request gave up waiting; its alerts are still being written",
			"request_id", requestID, "vendor", vendor, "alerts", len(alerts))
		return nil, ErrTimeout
	}
}

// QueueBytes is the size of accepted submissions not yet finished.
func (a *Allocator) QueueBytes() int64 { return a.bytes.Load() }

// sizeOf estimates a submission's memory: its alerts' fields, with each distinct description
// counted once (a Prometheus batch shares one description).
func sizeOf(alerts []model.Alert) int64 {
	var n int64
	seen := make(map[string]struct{}, 1)
	for _, al := range alerts {
		n += int64(len(al.MetricName) + len(al.UniqueIdentifier) + len(al.Service) + len(al.Category) +
			len(al.Environment) + len(al.Source) + len(al.Severity))
		if _, ok := seen[al.Description]; !ok {
			seen[al.Description] = struct{}{}
			n += int64(len(al.Description))
		}
	}
	return n
}

func (a *Allocator) reserveBytes(size int64) bool {
	for {
		cur := a.bytes.Load()
		if cur+size > a.cfg.QueueMaxBytes {
			return false
		}
		if a.bytes.CompareAndSwap(cur, cur+size) {
			return true
		}
	}
}

// releaseBytes returns a submission's size exactly once.
func (a *Allocator) releaseBytes(sub *submission) {
	if sub.released.CompareAndSwap(false, true) {
		a.bytes.Add(-sub.size)
	}
}

// finish releases a submission's bytes and delivers its result.
func (a *Allocator) finish(sub *submission, res Result) {
	a.releaseBytes(sub)
	sub.done <- res
}

func (a *Allocator) noteMemReject() {
	a.memRejects.Add(1)
	now := time.Now().UnixNano()
	last := a.memLogAt.Load()
	if now-last < int64(memLogEvery) || !a.memLogAt.CompareAndSwap(last, now) {
		return
	}
	a.logger.Warn("queue memory limit reached", "rejected", a.memRejects.Swap(0),
		"queue_bytes", a.bytes.Load(), "queue_max_bytes", a.cfg.QueueMaxBytes)
}

// Close stops accepting submissions, lets the claimer claim everything already queued, and
// waits for every write to finish or ctx to end. Safe to call more than once.
func (a *Allocator) Close(ctx context.Context) error {
	a.mu.Lock()
	if !a.closed {
		a.closed = true
		close(a.queue)
	}
	a.mu.Unlock()
	if dl, ok := ctx.Deadline(); ok {
		// Leave time for one filler write per unwritten id.
		a.drainOnce.Do(func() {
			a.stopAt.Store(dl.Add(-2 * a.cfg.QueryTimeout).UnixNano())
			close(a.drainCh)
		})
	}

	select {
	case <-a.claimerDone:
	case <-ctx.Done():
		a.logUnwritten()
		return fmt.Errorf("claimer did not drain: %w", ctx.Err())
	}
	written := make(chan struct{})
	go func() {
		a.writes.Wait()
		close(written)
	}()
	select {
	case <-written:
		return nil
	case <-ctx.Done():
		a.logUnwritten()
		return fmt.Errorf("writers did not finish: %w", ctx.Err())
	}
}

// maxLoggedIDs bounds the id list in the unwritten-ids log line.
const maxLoggedIDs = 100

// logUnwritten names the claimed ids left without a row, which alerts-core will wait
// gap_timeout on.
func (a *Allocator) logUnwritten() {
	a.pendingMu.Lock()
	seqs := make([]int64, 0, len(a.pending))
	for s := range a.pending {
		seqs = append(seqs, s)
	}
	a.pendingMu.Unlock()
	if len(seqs) == 0 {
		return
	}
	slices.Sort(seqs)
	ids := make([]string, 0, min(len(seqs), maxLoggedIDs))
	for _, s := range seqs[:min(len(seqs), maxLoggedIDs)] {
		ids = append(ids, cassandra.FormatID(s))
	}
	a.logger.Error("shutdown cut off writes; these ids have no row and alerts-core will wait gap_timeout on them",
		"count", len(seqs), "first_id", cassandra.FormatID(seqs[0]),
		"last_id", cassandra.FormatID(seqs[len(seqs)-1]), "ids", ids)
}

// reserveSlots blocks until k writer slots are held.
func (a *Allocator) reserveSlots(k int) {
	for range k {
		a.writeSem <- struct{}{}
	}
}

// tryReserveSlots takes k writer slots only if all are free now.
func (a *Allocator) tryReserveSlots(k int) bool {
	for i := range k {
		select {
		case a.writeSem <- struct{}{}:
		default:
			a.releaseSlots(i)
			return false
		}
	}
	return true
}

func (a *Allocator) releaseSlots(k int) {
	for range k {
		<-a.writeSem
	}
}

// claimLoop takes the first waiting submission, waits for writer slots for it, then adds
// whatever else is waiting while free slots and MaxBatch allow. Ids are claimed only for
// alerts that have a slot. A submission is never split: one larger than WriteConcurrency is
// claimed alone once every slot is free, and its extra alerts wait for slots after the claim.
func (a *Allocator) claimLoop() {
	defer close(a.claimerDone)
	var carry *submission
	for {
		first := carry
		carry = nil
		if first == nil {
			var ok bool
			if first, ok = <-a.queue; !ok {
				return
			}
		}
		held := min(len(first.alerts), a.cfg.WriteConcurrency)
		a.reserveSlots(held)
		batch := []*submission{first}
		n := len(first.alerts)
	gather:
		for n < a.cfg.MaxBatch {
			select {
			case sub, ok := <-a.queue:
				if !ok {
					break gather
				}
				if n+len(sub.alerts) > a.cfg.MaxBatch || !a.tryReserveSlots(len(sub.alerts)) {
					carry = sub
					break gather
				}
				batch = append(batch, sub)
				n += len(sub.alerts)
				held += len(sub.alerts)
			default:
				break gather
			}
		}

		claimStart := time.Now()
		start, attempts, err := a.claim(n)
		if err != nil {
			a.releaseSlots(held)
			a.logger.Error("claim failed; batch rejected, no ids claimed", "alerts", n,
				"submissions", len(batch), "claim_attempts", attempts, "error", err)
			for _, sub := range batch {
				a.finish(sub, Result{Err: ErrClaimFailed})
			}
			continue
		}
		a.logger.Info("batch claimed", "alerts", n, "submissions", len(batch),
			"first_id", cassandra.FormatID(start), "last_id", cassandra.FormatID(start+int64(n)-1),
			"claim_attempts", attempts, "claim_ms", time.Since(claimStart).Milliseconds(),
			"queue_len", len(a.queue), "queue_bytes", a.bytes.Load())
		a.pendingMu.Lock()
		for s := start; s < start+int64(n); s++ {
			a.pending[s] = struct{}{}
		}
		a.pendingMu.Unlock()
		a.writes.Add(1)
		go a.writeBatch(batch, start, held, claimStart)
	}
}

// claim reserves n consecutive ids with one compare-and-set and returns the first. A rejected
// compare-and-set returns the row's current value, which the retry uses directly. It starts
// from the value this replica last set, so alert_seq is only read when that is unknown; if
// another replica moved it since, the rejection costs what the read would have.
//
// A throttled read or compare-and-set was rejected by Cosmos DB and did not apply, so it is
// retried without counting an attempt. Any other compare-and-set error may still have applied
// on the server, leaving ids without rows; the retry can't tell, so that is logged.
func (a *Allocator) claim(n int) (start int64, attempts int, err error) {
	ctx := context.Background()
	giveUp := time.Now().Add(a.cfg.WriteDeadline)
	current := a.lastSeq
	if !a.seqKnown {
		if current, err = a.readSeq(ctx, giveUp); err != nil {
			return 0, 0, err
		}
	}
	a.seqKnown = false // set again only by an applied claim
	var lastErr error
	for attempt := 1; attempt <= a.cfg.ClaimMaxAttempts; {
		applied, seen, err := a.store.CompareAndSet(ctx, current, current+int64(n))
		switch {
		case cassandra.IsThrottled(err):
			if time.Now().After(giveUp) || a.shuttingDown() {
				return 0, attempt, err
			}
			a.sleep(retryWait(err))
			continue
		case err != nil:
			lastErr = err
			a.logger.Warn("compare-and-set errored; it may have applied, leaving a gap",
				"from", current, "to", current+int64(n), "attempt", attempt, "error", err)
			if current, err = a.readSeq(ctx, giveUp); err != nil {
				lastErr = err
			}
		case applied:
			a.lastSeq, a.seqKnown = current+int64(n), true
			return current + 1, attempt, nil
		default:
			current = seen
		}
		attempt++
		a.pause()
	}
	return 0, a.cfg.ClaimMaxAttempts, fmt.Errorf("gave up after %d attempts: %v", a.cfg.ClaimMaxAttempts, lastErr)
}

// readSeq reads alert_seq, waiting out throttling until giveUp.
func (a *Allocator) readSeq(ctx context.Context, giveUp time.Time) (int64, error) {
	for {
		seq, err := a.store.ReadSeq(ctx)
		if !cassandra.IsThrottled(err) || time.Now().After(giveUp) || a.shuttingDown() {
			return seq, err
		}
		a.sleep(retryWait(err))
	}
}

func (a *Allocator) pause() {
	if a.cfg.ClaimJitter > 0 {
		time.Sleep(rand.N(a.cfg.ClaimJitter))
	}
}

// retryWait is the RetryAfter Cosmos DB asked for, plus jitter.
func retryWait(err error) time.Duration {
	return cassandra.RetryAfter(err) + rand.N(throttleJitter)
}

// sleep waits d, cut short to the shutdown stop time once a drain starts.
func (a *Allocator) sleep(d time.Duration) {
	start := time.Now()
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return
	case <-a.drainCh:
	}
	rest := min(d-time.Since(start), time.Until(time.Unix(0, a.stopAt.Load())))
	if rest > 0 {
		time.Sleep(rest)
	}
}

// shuttingDown reports whether a shutdown drain has reached its stop time.
func (a *Allocator) shuttingDown() bool {
	at := a.stopAt.Load()
	return at != 0 && time.Now().UnixNano() >= at
}

// batchStats is shared by one batch's writers for the "batch written" line.
type batchStats struct {
	ru        cassandra.RUMeter
	throttled atomic.Int64
}

type alertJob struct {
	sub      *submission
	index    int
	seq      int64
	deadline time.Time
	stats    *batchStats
}

// writeBatch writes every alert in parallel. The claimer already holds `held` writer slots for
// this batch; alerts beyond that (a submission larger than WriteConcurrency) wait for a slot.
// It replies to each submitter with its ids and wakes alerts-core once if anything was stored.
func (a *Allocator) writeBatch(batch []*submission, start int64, held int, claimedAt time.Time) {
	defer a.writes.Done()
	writeStart := time.Now()
	stats := &batchStats{}
	deadline := claimedAt.Add(a.cfg.WriteDeadline)

	type outcome struct {
		ids    []string
		failed bool
	}
	outcomes := make([]outcome, len(batch))
	var mu sync.Mutex
	var wg sync.WaitGroup
	stored := false

	seq := start
	k := 0
	for si, sub := range batch {
		outcomes[si].ids = make([]string, len(sub.alerts))
		for i := range sub.alerts {
			job := alertJob{sub: sub, index: i, seq: seq, deadline: deadline, stats: stats}
			seq++
			if k >= held {
				a.writeSem <- struct{}{}
			}
			k++
			wg.Add(1)
			go func(si int) {
				defer wg.Done()
				defer func() { <-a.writeSem }()
				id, ok := a.writeOne(job)
				mu.Lock()
				outcomes[si].ids[job.index] = id
				if ok {
					stored = true
				} else {
					outcomes[si].failed = true
				}
				mu.Unlock()
			}(si)
		}
	}
	wg.Wait()

	failed := 0
	for _, o := range outcomes {
		if o.failed {
			failed++
		}
	}
	attrs := []any{"alerts", seq - start, "first_id", cassandra.FormatID(start),
		"failed_submissions", failed, "write_ms", time.Since(writeStart).Milliseconds(),
		"throttled", stats.throttled.Load()}
	if ru, ok := stats.ru.Total(); ok {
		attrs = append(attrs, "total_ru", ru)
	}
	a.logger.Info("batch written", attrs...)

	for si, sub := range batch {
		if outcomes[si].failed {
			a.finish(sub, Result{IDs: outcomes[si].ids, Err: ErrStoreFailed})
		} else {
			a.finish(sub, Result{IDs: outcomes[si].ids})
		}
	}
	if stored && a.waker != nil {
		a.waker.Wake()
	}
}

// writeOne writes one alert under its claimed id and, with ReadBack, confirms it by reading it
// back. Throttled inserts and read-backs are retried on the same id until the write deadline without counting
// an attempt; other errors get InsertAttempts attempts. Then it falls back to a filler row.
// ok reports whether the real alert was stored.
func (a *Allocator) writeOne(job alertJob) (id string, ok bool) {
	defer func() {
		a.pendingMu.Lock()
		delete(a.pending, job.seq)
		a.pendingMu.Unlock()
	}()
	ctx := cassandra.WithRUMeter(context.Background(), &job.stats.ru) // detached from the request
	id = cassandra.FormatID(job.seq)
	alert := job.sub.alerts[job.index]
	if alert.UniqueIdentifier == "" {
		a.logger.Warn("vendor sent no id; using the alert id as unique_identifier, so this alert "+
			"won't merge with repeats or resolve on recovery",
			"request_id", job.sub.requestID, "vendor", job.sub.vendor, "alt_id", id)
		alert.UniqueIdentifier = id
	}
	body, err := json.Marshal(alert)
	if err != nil {
		// Unreachable for a struct of strings; handled so the id still gets a row.
		return id, a.fail(job, id, alert, err)
	}

	w := writeState{a: a, job: job, id: id}
	failures := 0
	delay := a.cfg.InsertBaseDelay
	inserted := false
	for {
		if !inserted {
			if time.Until(job.deadline) < 2*a.cfg.QueryTimeout || a.shuttingDown() {
				return id, a.fail(job, id, alert, w.stopErr())
			}
			err := a.store.Insert(ctx, id, job.sub.vendor, string(body))
			w.attempts++
			if cassandra.IsThrottled(err) {
				w.throttled(err)
				continue
			}
			if err == nil && !a.cfg.ReadBack {
				w.stored()
				return id, true
			}
			if err == nil {
				inserted = true
			} else if w.failed(err, &failures, &delay) {
				return id, a.fail(job, id, alert, err)
			} else {
				continue
			}
		}
		visible, err := a.store.Exists(ctx, id)
		switch {
		case err == nil && visible:
			w.stored()
			return id, true
		case cassandra.IsThrottled(err):
			if time.Now().After(job.deadline) || a.shuttingDown() {
				return id, a.fail(job, id, alert, w.stopErr())
			}
			w.throttled(err)
			continue // read back again; the insert already went through
		case err == nil:
			err = fmt.Errorf("row %s not visible after insert", id)
		}
		inserted = false // insert again, as for any failed attempt
		if w.failed(err, &failures, &delay) {
			return id, a.fail(job, id, alert, err)
		}
	}
}

// writeState tracks one alert's retries for logging.
type writeState struct {
	a             *Allocator
	job           alertJob
	id            string
	attempts      int
	throttles     int
	firstThrottle time.Time
}

func (w *writeState) throttled(err error) {
	w.job.stats.throttled.Add(1)
	if w.throttles == 0 {
		w.firstThrottle = time.Now()
		w.a.logger.Warn("Cosmos DB throttled the write; retrying on the same id",
			"request_id", w.job.sub.requestID, "vendor", w.job.sub.vendor, "alt_id", w.id,
			"retry_after", cassandra.RetryAfter(err))
	}
	w.throttles++
	w.a.sleep(retryWait(err))
}

// failed counts a non-throttle error, waits before the next attempt, and reports whether the
// attempts are used up.
func (w *writeState) failed(err error, failures *int, delay *time.Duration) bool {
	*failures++
	w.a.logger.Warn("insert failed, retrying on the same id", "request_id", w.job.sub.requestID,
		"vendor", w.job.sub.vendor, "alt_id", w.id, "attempt", *failures, "error", err)
	if *failures >= w.a.cfg.InsertAttempts {
		return true
	}
	w.a.sleep(*delay)
	*delay *= 2
	return false
}

func (w *writeState) stored() {
	if w.throttles > 0 {
		w.a.logger.Info("alert stored after throttling", "request_id", w.job.sub.requestID,
			"vendor", w.job.sub.vendor, "alt_id", w.id, "attempts", w.attempts,
			"waited_ms", time.Since(w.firstThrottle).Milliseconds())
	}
}

func (w *writeState) stopErr() error {
	if w.a.shuttingDown() {
		return errors.New("shutdown stopped retries before the alert was stored")
	}
	if w.throttles > 0 {
		return errors.New("write deadline passed while Cosmos DB was throttling")
	}
	return errors.New("write deadline passed before the alert could be written")
}

// fail writes the filler row, reports the failure, and returns writeOne's ok. The filler never
// overwrites a row: if the alert did land (a timed-out insert, or a read-back Cosmos missed),
// it is kept and counted as stored. A throttled filler is retried until the write deadline
// plus fillerGrace; other errors get InsertAttempts attempts. During a shutdown drain it gets
// one attempt.
func (a *Allocator) fail(job alertJob, id string, alert model.Alert, cause error) bool {
	filler := fillerPrefix + cause.Error()
	fillerDeadline := job.deadline.Add(fillerGrace)
	var existing string
	var fillerErr error
	failures := 0
	delay := a.cfg.InsertBaseDelay
	for {
		applied, prev, err := a.store.InsertFiller(context.Background(), id, job.sub.vendor, filler)
		fillerErr = err
		if err == nil {
			if !applied {
				existing = prev
			}
			break
		}
		if a.shuttingDown() {
			break
		}
		if cassandra.IsThrottled(err) {
			if time.Now().Add(cassandra.RetryAfter(err)).After(fillerDeadline) {
				break
			}
			job.stats.throttled.Add(1)
			a.sleep(retryWait(err))
			continue
		}
		failures++
		a.logger.Warn("filler row failed, retrying", "request_id", job.sub.requestID,
			"vendor", job.sub.vendor, "alt_id", id, "attempt", failures, "error", err)
		if failures >= a.cfg.InsertAttempts {
			break
		}
		a.sleep(delay)
		delay *= 2
	}
	if existing != "" && !strings.HasPrefix(existing, fillerPrefix) {
		a.logger.Warn("alert stored although its write reported a failure; filler not written",
			"request_id", job.sub.requestID, "vendor", job.sub.vendor, "alt_id", id, "error", cause)
		return true
	}
	if fillerErr != nil {
		// Cassandra is likely down: alerts-core will wait gap_timeout on this id.
		a.logger.Error("alert NOT stored and filler row failed; alerts-core will stall on this id until gap_timeout",
			"request_id", job.sub.requestID, "vendor", job.sub.vendor, "alt_id", id,
			"alert", alert, "error", cause, "filler_error", fillerErr)
	} else {
		a.logger.Error("alert NOT stored; filler row written so alerts-core skips the id",
			"request_id", job.sub.requestID, "vendor", job.sub.vendor, "alt_id", id,
			"alert", alert, "error", cause)
	}
	if a.notifier != nil {
		a.notifier.StoreFailed(StoreFailure{
			Vendor: job.sub.vendor, RequestID: job.sub.requestID, AltID: id,
			Alert: alert, Err: cause, FillerWritten: fillerErr == nil,
		})
	}
	return false
}
