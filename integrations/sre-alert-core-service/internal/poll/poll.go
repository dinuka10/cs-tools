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

// Package poll discovers new alerts by comparing alert_seq against alert_cursor; ping wakes it early, a ticker is the backstop.
package poll

import (
	"context"
	"hash/fnv"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gocql/gocql"
	"golang.org/x/sync/errgroup"

	"alert-core-service/internal/cassandra"
	"alert-core-service/internal/engine"
	"alert-core-service/internal/model"
)

const (
	alertSeqTable = "alert_seq"
	cursorTable   = "alert_cursor"
	alertIDPrefix = "ALT"
	alertIDWidth  = 9
)

// Leader reports whether this replica may process alerts, preventing duplicate incident notifications across replicas.
type Leader interface {
	IsLeader() bool
}

// Settings tunes a Poller's cadence and per-cycle concurrency.
type Settings struct {
	// Interval is the backstop cadence between cycles; a ping normally wakes the poller sooner.
	Interval time.Duration
	// Concurrency is the number of fingerprint-sharded workers handling alerts in parallel.
	Concurrency int
	// ReadConcurrency bounds the parallel alert-row reads at the start of each cycle.
	ReadConcurrency int
	// MaxWindow caps how many alert ids one window processes, bounding memory under large bursts.
	MaxWindow int
	// NotifySweepInterval is the retry cadence for unconfirmed CSM/Chat notifications, independent of and concurrent-safe with the alert cycle.
	NotifySweepInterval time.Duration
	// GapTimeout is how long an alert id may stay missing before it's skipped; a whole gap is skipped together after one GapTimeout, zero disables skipping.
	GapTimeout time.Duration
}

// Poller periodically (and on demand) processes every alert id issued since its last confirmed position.
type Poller struct {
	logger   *slog.Logger
	session  *gocql.Session
	engine   *engine.Engine
	leader   Leader
	settings Settings
	wake     chan struct{}
	sweeping atomic.Bool
	// wg tracks in-flight sweep goroutines so Run doesn't return, and callers don't see it drained, mid-sweep.
	wg sync.WaitGroup
	// gaps backs GapTimeout; only touched from the single goroutine running cycle, so it needs no lock.
	gaps gapTracker
}

// New seeds alert_seq and cursor rows so a fresh deployment's first cycle doesn't fail with "not found" forever.
func New(logger *slog.Logger, session *gocql.Session, e *engine.Engine, leader Leader, settings Settings) (*Poller, error) {
	if err := cassandra.SeedSeq(context.Background(), session, alertSeqTable); err != nil {
		return nil, err
	}
	if err := cassandra.SeedSeq(context.Background(), session, cursorTable); err != nil {
		return nil, err
	}
	return &Poller{
		logger:   logger,
		session:  session,
		engine:   e,
		leader:   leader,
		settings: settings,
		wake:     make(chan struct{}, 1),
		gaps:     gapTracker{},
	}, nil
}

// Wake nudges the poller to run now instead of waiting; non-blocking, so a ping burst collapses into one cycle.
func (p *Poller) Wake() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// Run processes alerts on ping/interval until ctx cancels, and doesn't return until launched RetrySweep goroutines finish too.
func (p *Poller) Run(ctx context.Context) {
	ticker := time.NewTicker(p.settings.Interval)
	defer ticker.Stop()
	notifyTicker := time.NewTicker(p.settings.NotifySweepInterval)
	defer notifyTicker.Stop()
	defer p.wg.Wait()

	p.cycle(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.cycle(ctx)
		case <-p.wake:
			p.cycle(ctx)
		case <-notifyTicker.C:
			if p.leader.IsLeader() && p.sweeping.CompareAndSwap(false, true) {
				// Runs in a separate goroutine so a slow CSM/Chat outage never delays cycle(); guard prevents overlapping sweeps.
				p.wg.Add(1)
				go func() {
					defer p.wg.Done()
					defer p.sweeping.Store(false)
					p.engine.RetrySweep(ctx, p.leader.IsLeader)
				}()
			}
		}
	}
}

// cycle drains alert ids from cursor to latest in bounded windows, advancing the durable cursor after each completed prefix.
func (p *Poller) cycle(ctx context.Context) {
	if !p.leader.IsLeader() {
		clear(p.gaps) // timers only mean something for the leader that saw the ids missing
		return
	}

	latest, err := cassandra.ReadSeq(ctx, p.session, alertSeqTable)
	if err != nil {
		p.logger.Error("failed to read alert_seq", "error", err)
		return
	}
	cursor, err := cassandra.ReadSeq(ctx, p.session, cursorTable)
	if err != nil {
		p.logger.Error("failed to read cursor", "error", err)
		return
	}

	for cursor < latest {
		if !p.leader.IsLeader() {
			p.logger.Warn("lost leadership mid-cycle, stopping", "cursor", cursor)
			clear(p.gaps)
			return
		}
		next := p.processWindow(ctx, cursor, latest)
		p.gaps.pruneThrough(max(next, cursor))
		if next <= cursor {
			// No progress: window head not visible yet, or cursor moved elsewhere; wait for next tick/ping.
			return
		}
		cursor = next
	}
}

// processWindow handles one window of alert ids and returns the new cursor; never blocks on an unready id, deferring it.
func (p *Poller) processWindow(ctx context.Context, cursor, latest int64) int64 {
	base := cursor + 1
	end := min(latest, cursor+int64(p.settings.MaxWindow))
	n := int(end - base + 1)

	// Stage 1: read + normalize every id in the window concurrently, into disjoint slots.
	slots := p.readWindow(ctx, base, n)

	// Stage 2: decide per id, skipping terminal/GapTimeout-expired ids, stopping at a newer missing id or read error.
	d := decideWindow(slots, base, time.Now(), p.settings.GapTimeout, p.gaps)
	if len(d.skipped) > 0 {
		p.logSkipped(d.skipped)
	}
	outcomes := d.outcomes

	p.handleSharded(ctx, base, slots, outcomes, d.readStop)

	// Advance across the leading run of completed ids (Processed/Failed), stopping at the first Retry.
	completed := contiguousCompleted(outcomes)
	if completed == 0 {
		return cursor
	}
	target := base + int64(completed) - 1

	applied, err := cassandra.AdvanceSeqTo(ctx, p.session, cursorTable, cursor, target)
	if err != nil {
		p.logger.Error("failed to advance cursor", "from", cursor, "to", target, "error", err)
		return cursor
	}
	if !applied {
		p.logger.Warn("cursor advanced concurrently, stopping cycle")
		return cursor
	}
	p.logger.Info("processed alert window", "from", base, "to", target, "count", completed)
	return target
}

// maxLoggedSkips bounds the id list in the skipped-gap log line.
const maxLoggedSkips = 50

// logSkipped reports one window's skipped ids in a single line.
func (p *Poller) logSkipped(skipped []int64) {
	ids := make([]string, 0, min(len(skipped), maxLoggedSkips))
	for _, seq := range skipped[:min(len(skipped), maxLoggedSkips)] {
		ids = append(ids, cassandra.FormatSeq(alertIDPrefix, alertIDWidth, seq))
	}
	p.logger.Error("alert ids missing beyond gap timeout, skipping to unblock the pipeline",
		"count", len(skipped),
		"first_id", cassandra.FormatSeq(alertIDPrefix, alertIDWidth, skipped[0]),
		"last_id", cassandra.FormatSeq(alertIDPrefix, alertIDWidth, skipped[len(skipped)-1]),
		"ids", ids, "gap_timeout", p.settings.GapTimeout)
}

// prepared holds the result of reading and normalizing one alert id; notFound is only meaningful when !ready.
type prepared struct {
	alert    model.Alert
	fp       string
	outcome  engine.Outcome
	ready    bool
	notFound bool
}

// readWindow reads n alert ids starting at base concurrently, bounded by ReadConcurrency; Prepare never errors itself, so g.Wait()'s error is always nil.
func (p *Poller) readWindow(ctx context.Context, base int64, n int) []prepared {
	slots := make([]prepared, n)
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(p.settings.ReadConcurrency)
	for i := range n {
		g.Go(func() error {
			id := cassandra.FormatSeq(alertIDPrefix, alertIDWidth, base+int64(i))
			alert, fp, outcome, ready, notFound := p.engine.Prepare(gctx, id)
			slots[i] = prepared{alert: alert, fp: fp, outcome: outcome, ready: ready, notFound: notFound}
			return nil
		})
	}
	_ = g.Wait()
	return slots
}

// handleSharded processes ready ids on a fingerprint-sharded pool and blocks until done, so the caller can safely advance the cursor.
func (p *Poller) handleSharded(ctx context.Context, base int64, slots []prepared, outcomes []engine.Outcome, readStop int) {
	workers := min(p.settings.Concurrency, readStop)
	if workers <= 0 {
		return // nothing ready to handle
	}

	// Size each worker's queue to exactly its assignment count so dispatch never blocks.
	shardOf := make([]int, readStop)
	counts := make([]int, workers)
	for i := range readStop {
		if !slots[i].ready {
			continue
		}
		s := shard(slots[i].fp, workers)
		shardOf[i] = s
		counts[s]++
	}

	type task struct {
		idx int
		id  string
	}
	queues := make([]chan task, workers)
	var wg sync.WaitGroup
	for w := range workers {
		queues[w] = make(chan task, counts[w])
		wg.Add(1)
		go func(q chan task) {
			defer wg.Done()
			for t := range q {
				outcomes[t.idx] = p.engine.Handle(ctx, t.id, slots[t.idx].alert)
			}
		}(queues[w])
	}
	for i := range readStop {
		if !slots[i].ready {
			continue
		}
		id := cassandra.FormatSeq(alertIDPrefix, alertIDWidth, base+int64(i))
		queues[shardOf[i]] <- task{idx: i, id: id}
	}
	for w := range workers {
		close(queues[w])
	}
	wg.Wait()
}

// contiguousCompleted returns the leading run length of completed outcomes, stopping at the first Retry, for advancing the cursor.
func contiguousCompleted(outcomes []engine.Outcome) int {
	for i, o := range outcomes {
		if o == engine.Retry {
			return i
		}
	}
	return len(outcomes)
}

// shard maps a fingerprint to a worker index via FNV-1a so an incident's alerts land on the same worker.
func shard(fingerprint string, workers int) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(fingerprint))
	return int(h.Sum32() % uint32(workers))
}
