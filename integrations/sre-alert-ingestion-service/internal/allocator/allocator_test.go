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

package allocator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sre-alert-ingestion-service/internal/model"
)

type fakeRow struct{ vendor, alert string }

// fakeStore is an in-memory alert_seq + alerts table with knobs for the failure cases.
type fakeStore struct {
	mu      sync.Mutex
	seq     int64
	rows    map[string]fakeRow
	inserts map[string]int // insert calls per id, filler included

	casCalls atomic.Int64
	reads    atomic.Int64 // ReadSeq calls
	exists   atomic.Int64 // read-backs
	claims   atomic.Int64 // applied compare-and-sets
	casDelay time.Duration

	// stealNext rejects the next N compare-and-sets by advancing seq by stealBy first,
	// as another replica winning the race would.
	stealNext int
	stealBy   int64
	// rejectAll rejects every compare-and-set (claim exhaustion).
	rejectAll bool

	// failRealInsert fails inserts of real alerts (not filler rows) for which it returns true.
	failRealInsert func(alert string) bool
	failAllInserts bool
	// failFillers fails this many filler inserts before letting them through.
	failFillers int
	// insertGate, if set, blocks every Insert until closed.
	insertGate chan struct{}
	// hideOnce makes the first read-back of an id miss, as Cosmos DB sometimes does.
	hideOnce map[string]bool
	// neverVisible makes every read-back miss.
	neverVisible bool

	// throttleInserts throttles the next N real inserts; throttleAllInserts throttles all of them.
	throttleInserts    int
	throttleAllInserts bool
	// throttleCAS throttles the next N compare-and-sets.
	throttleCAS int

	// readGate, if set, blocks ReadSeq until closed; readEntered is signalled on entry.
	readGate    chan struct{}
	readEntered chan struct{}
}

// errThrottled is a Cosmos DB 429 asking for a 1 ms wait, to keep tests fast.
var errThrottled = errors.New("Request rate is large. More Request Units may be needed. RetryAfterMs=1")

func newFakeStore() *fakeStore {
	return &fakeStore{rows: map[string]fakeRow{}, inserts: map[string]int{}, hideOnce: map[string]bool{}}
}

func (f *fakeStore) ReadSeq(context.Context) (int64, error) {
	f.reads.Add(1)
	if f.readEntered != nil {
		select {
		case f.readEntered <- struct{}{}:
		default:
		}
	}
	if f.readGate != nil {
		<-f.readGate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seq, nil
}

func (f *fakeStore) CompareAndSet(_ context.Context, from, to int64) (bool, int64, error) {
	f.casCalls.Add(1)
	if f.casDelay > 0 {
		time.Sleep(f.casDelay)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.throttleCAS > 0 {
		f.throttleCAS--
		return false, 0, errThrottled
	}
	if f.stealNext > 0 {
		f.stealNext--
		f.seq += f.stealBy
	}
	if f.rejectAll || f.seq != from {
		return false, f.seq, nil
	}
	f.seq = to
	f.claims.Add(1)
	return true, to, nil
}

func (f *fakeStore) Insert(_ context.Context, id, vendor, alert string) error {
	if f.insertGate != nil {
		<-f.insertGate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inserts[id]++
	isFiller := strings.HasPrefix(alert, fillerPrefix)
	if !isFiller && (f.throttleAllInserts || f.throttleInserts > 0) {
		if f.throttleInserts > 0 {
			f.throttleInserts--
		}
		return errThrottled
	}
	if isFiller && f.failFillers > 0 {
		f.failFillers--
		return errors.New("write timeout")
	}
	if f.failAllInserts || (!isFiller && f.failRealInsert != nil && f.failRealInsert(alert)) {
		return errors.New("write timeout")
	}
	f.rows[id] = fakeRow{vendor: vendor, alert: alert}
	return nil
}

func (f *fakeStore) InsertFiller(ctx context.Context, id, vendor, filler string) (bool, string, error) {
	f.mu.Lock()
	row, ok := f.rows[id]
	f.mu.Unlock()
	if ok {
		f.mu.Lock()
		f.inserts[id]++
		f.mu.Unlock()
		return false, row.alert, nil
	}
	return true, "", f.Insert(ctx, id, vendor, filler)
}

func (f *fakeStore) Exists(_ context.Context, id string) (bool, error) {
	f.exists.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.neverVisible {
		return false, nil
	}
	if f.hideOnce[id] {
		delete(f.hideOnce, id)
		return false, nil
	}
	_, ok := f.rows[id]
	return ok, nil
}

type recordingNotifier struct {
	mu       sync.Mutex
	failures []StoreFailure
}

func (n *recordingNotifier) StoreFailed(f StoreFailure) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.failures = append(n.failures, f)
}

type countingWaker struct{ n atomic.Int64 }

func (w *countingWaker) Wake() { w.n.Add(1) }

func testConfig() Config {
	return Config{
		QueueSize: 5000, MaxBatch: 200, WriteConcurrency: 64, ClaimMaxAttempts: 20,
		InsertAttempts: 3, InsertBaseDelay: time.Millisecond, ClaimJitter: time.Millisecond,
		QueueMaxBytes: 1 << 30, QueryTimeout: time.Millisecond, WriteDeadline: time.Minute,
	}
}

func newTestAllocator(t *testing.T, store *fakeStore, n FailureNotifier, w Waker, cfg Config) *Allocator {
	t.Helper()
	a := New(slog.New(slog.NewTextHandler(io.Discard, nil)), store, n, w, cfg)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = a.Close(ctx)
	})
	return a
}

func alert(service, uid string) model.Alert {
	return model.Alert{Service: service, MetricName: "HighCPU", Severity: "Critical", Source: "Test", UniqueIdentifier: uid}
}

func seqOf(t *testing.T, id string) int64 {
	t.Helper()
	if !strings.HasPrefix(id, "ALT") || len(id) != 12 {
		t.Fatalf("id %q is not ALT + 9 digits", id)
	}
	n, err := strconv.ParseInt(id[3:], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestConcurrentSubmits_UniqueConsecutiveIDsWithFewClaims(t *testing.T) {
	store := newFakeStore()
	store.casDelay = 2 * time.Millisecond // a real claim takes a few ms; lets the queue fill
	a := newTestAllocator(t, store, nil, nil, testConfig())

	const total = 1000
	ids := make([]string, total)
	var wg sync.WaitGroup
	for i := range total {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := a.Submit(context.Background(), "aws", "req", []model.Alert{alert("svc", "u"+strconv.Itoa(i))})
			if err != nil {
				t.Errorf("Submit: %v", err)
				return
			}
			ids[i] = got[0]
		}()
	}
	wg.Wait()

	seen := map[int64]bool{}
	for _, id := range ids {
		n := seqOf(t, id)
		if seen[n] {
			t.Fatalf("id %s issued twice", id)
		}
		seen[n] = true
	}
	for n := int64(1); n <= total; n++ {
		if !seen[n] {
			t.Fatalf("gap: id %d never issued", n)
		}
	}
	if len(store.rows) != total {
		t.Errorf("rows = %d, want %d", len(store.rows), total)
	}
	if store.seq != total {
		t.Errorf("alert_seq = %d, want %d", store.seq, total)
	}
	if c := store.claims.Load(); c >= total/10 {
		t.Errorf("claims = %d, want far fewer than %d", c, total)
	} else {
		t.Logf("%d alerts claimed in %d compare-and-sets", total, c)
	}
}

func TestLostCompareAndSet_IsRetriedWithReturnedValue(t *testing.T) {
	store := newFakeStore()
	store.stealNext, store.stealBy = 3, 10 // another replica wins three times, 10 ids each
	a := newTestAllocator(t, store, nil, nil, testConfig())

	ids, err := a.Submit(context.Background(), "aws", "req", []model.Alert{alert("svc", "u1"), alert("svc", "u2")})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if ids[0] != "ALT000000031" || ids[1] != "ALT000000032" {
		t.Errorf("ids = %v, want ALT000000031, ALT000000032 after 30 stolen ids", ids)
	}
	if c := store.casCalls.Load(); c != 4 {
		t.Errorf("compare-and-sets = %d, want 4 (3 lost + 1 won)", c)
	}
}

func TestClaimExhausted_FailsWithoutWritingRows(t *testing.T) {
	store := newFakeStore()
	store.rejectAll = true
	cfg := testConfig()
	cfg.ClaimMaxAttempts = 3
	a := newTestAllocator(t, store, nil, nil, cfg)

	if _, err := a.Submit(context.Background(), "aws", "req", []model.Alert{alert("svc", "u")}); !errors.Is(err, ErrClaimFailed) {
		t.Fatalf("err = %v, want ErrClaimFailed", err)
	}
	if len(store.rows) != 0 {
		t.Errorf("rows = %d, want none: nothing was claimed", len(store.rows))
	}
}

func TestInsertFailure_WritesFillerAndFails(t *testing.T) {
	store := newFakeStore()
	store.failRealInsert = func(a string) bool { return strings.Contains(a, `"service":"bad"`) }
	notifier := &recordingNotifier{}
	a := newTestAllocator(t, store, notifier, nil, testConfig())

	ids, err := a.Submit(context.Background(), "prometheus", "req-9", []model.Alert{alert("good", "u1"), alert("bad", "u2")})
	if !errors.Is(err, ErrStoreFailed) {
		t.Fatalf("err = %v, want ErrStoreFailed (503)", err)
	}
	if len(ids) != 2 {
		t.Fatalf("ids = %v", ids)
	}
	good, bad := store.rows[ids[0]], store.rows[ids[1]]
	if !strings.Contains(good.alert, `"service":"good"`) {
		t.Errorf("good row = %q", good.alert)
	}
	if !strings.HasPrefix(bad.alert, "VOID: ") || bad.vendor != "prometheus" {
		t.Errorf("failed id should hold a filler row, got %+v", bad)
	}
	var probe model.Alert
	if json.Unmarshal([]byte(bad.alert), &probe) == nil {
		t.Error("filler row must not parse as an alert, or alerts-core would process it")
	}
	if n := store.inserts[ids[1]]; n != 4 {
		t.Errorf("insert calls on the failed id = %d, want 3 attempts + 1 filler", n)
	}
	if len(notifier.failures) != 1 {
		t.Fatalf("notifications = %d, want 1", len(notifier.failures))
	}
	f := notifier.failures[0]
	if f.AltID != ids[1] || f.Vendor != "prometheus" || f.RequestID != "req-9" || !f.FillerWritten || f.Alert.Service != "bad" {
		t.Errorf("failure = %+v", f)
	}
}

func TestInsertAndFillerBothFail_ReportsFillerMissing(t *testing.T) {
	store := newFakeStore()
	store.failAllInserts = true
	notifier := &recordingNotifier{}
	a := newTestAllocator(t, store, notifier, nil, testConfig())

	if _, err := a.Submit(context.Background(), "aws", "req", []model.Alert{alert("svc", "u")}); !errors.Is(err, ErrStoreFailed) {
		t.Fatalf("err = %v, want ErrStoreFailed", err)
	}
	if len(notifier.failures) != 1 || notifier.failures[0].FillerWritten {
		t.Errorf("failures = %+v, want one with FillerWritten=false", notifier.failures)
	}
	if n := store.inserts["ALT000000001"]; n != 6 {
		t.Errorf("insert calls = %d, want 3 attempts + 3 filler attempts", n)
	}
}

func TestFillerRetried_UntilWritten(t *testing.T) {
	store := newFakeStore()
	store.failRealInsert = func(string) bool { return true }
	store.failFillers = 2
	notifier := &recordingNotifier{}
	a := newTestAllocator(t, store, notifier, nil, testConfig())

	ids, err := a.Submit(context.Background(), "aws", "req", []model.Alert{alert("svc", "u")})
	if !errors.Is(err, ErrStoreFailed) {
		t.Fatalf("err = %v, want ErrStoreFailed", err)
	}
	if !strings.HasPrefix(store.rows[ids[0]].alert, fillerPrefix) {
		t.Errorf("row = %+v, want the filler after its third attempt", store.rows[ids[0]])
	}
	if len(notifier.failures) != 1 || !notifier.failures[0].FillerWritten {
		t.Errorf("failures = %+v, want FillerWritten=true", notifier.failures)
	}
}

func TestReadBackMiss_RetriesOnSameID(t *testing.T) {
	store := newFakeStore()
	store.hideOnce["ALT000000001"] = true
	cfg := testConfig()
	cfg.ReadBack = true
	a := newTestAllocator(t, store, nil, nil, cfg)

	ids, err := a.Submit(context.Background(), "aws", "req", []model.Alert{alert("svc", "u")})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if ids[0] != "ALT000000001" || store.inserts["ALT000000001"] != 2 {
		t.Errorf("ids = %v, inserts = %d; want the same id re-inserted once", ids, store.inserts["ALT000000001"])
	}
}

func TestNoReadBack_ByDefault(t *testing.T) {
	store := newFakeStore()
	a := newTestAllocator(t, store, nil, nil, testConfig())
	if _, err := a.Submit(context.Background(), "aws", "req", []model.Alert{alert("svc", "u")}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if n := store.exists.Load(); n != 0 {
		t.Errorf("read-backs = %d, want 0 with ReadBack off", n)
	}
}

func TestClaim_ReadsSeqOnlyOnce(t *testing.T) {
	store := newFakeStore()
	a := newTestAllocator(t, store, nil, nil, testConfig())
	for i := range 3 {
		ids, err := a.Submit(context.Background(), "aws", "req", []model.Alert{alert("svc", "u")})
		if err != nil || ids[0] != fmt.Sprintf("ALT%09d", i+1) {
			t.Fatalf("Submit %d = %v, %v", i, ids, err)
		}
	}
	if n := store.reads.Load(); n != 1 {
		t.Errorf("alert_seq reads = %d, want 1: later claims start from the value last set", n)
	}
}

func TestClaim_StaleCachedSeqUsesReturnedValue(t *testing.T) {
	store := newFakeStore()
	a := newTestAllocator(t, store, nil, nil, testConfig())
	if _, err := a.Submit(context.Background(), "aws", "req", []model.Alert{alert("svc", "u")}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	store.mu.Lock()
	store.seq += 5 // another replica claimed 2..6
	store.mu.Unlock()
	ids, err := a.Submit(context.Background(), "aws", "req", []model.Alert{alert("svc", "v")})
	if err != nil || ids[0] != "ALT000000007" {
		t.Fatalf("ids = %v, err = %v; want ALT000000007", ids, err)
	}
	if n := store.reads.Load(); n != 1 {
		t.Errorf("alert_seq reads = %d, want 1: the rejection returns the current value", n)
	}
}

func TestBatchSubmission_GetsConsecutiveIDsInOrder(t *testing.T) {
	store := newFakeStore()
	store.seq = 41
	a := newTestAllocator(t, store, nil, nil, testConfig())

	batch := []model.Alert{alert("a", "1"), alert("b", "2"), alert("c", "3")}
	ids, err := a.Submit(context.Background(), "prometheus", "req", batch)
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		if seqOf(t, id) != int64(42+i) {
			t.Fatalf("ids = %v, want ALT000000042..44", ids)
		}
		if !strings.Contains(store.rows[id].alert, `"service":"`+batch[i].Service+`"`) {
			t.Errorf("row %s holds the wrong alert: %s", id, store.rows[id].alert)
		}
	}
}

func TestLargeSubmissionIsNotSplit(t *testing.T) {
	store := newFakeStore()
	cfg := testConfig()
	cfg.MaxBatch = 2
	a := newTestAllocator(t, store, nil, nil, cfg)

	batch := make([]model.Alert, 5)
	for i := range batch {
		batch[i] = alert("svc", strconv.Itoa(i))
	}
	ids, err := a.Submit(context.Background(), "prometheus", "req", batch)
	if err != nil {
		t.Fatal(err)
	}
	if seqOf(t, ids[0]) != 1 || seqOf(t, ids[4]) != 5 || store.claims.Load() != 1 {
		t.Errorf("ids = %v, claims = %d; want one claim of 5 consecutive ids", ids, store.claims.Load())
	}
}

func TestMissingUniqueIdentifier_UsesOwnAltID(t *testing.T) {
	store := newFakeStore()
	a := newTestAllocator(t, store, nil, nil, testConfig())

	ids, err := a.Submit(context.Background(), "datadog", "req", []model.Alert{alert("svc", "")})
	if err != nil {
		t.Fatal(err)
	}
	var stored model.Alert
	if err := json.Unmarshal([]byte(store.rows[ids[0]].alert), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.UniqueIdentifier != ids[0] {
		t.Errorf("unique_identifier = %q, want %q", stored.UniqueIdentifier, ids[0])
	}
}

func TestWakesOncePerBatch(t *testing.T) {
	store := newFakeStore()
	waker := &countingWaker{}
	a := newTestAllocator(t, store, nil, waker, testConfig())

	if _, err := a.Submit(context.Background(), "aws", "req", []model.Alert{alert("a", "1"), alert("b", "2")}); err != nil {
		t.Fatal(err)
	}
	if n := waker.n.Load(); n != 1 {
		t.Errorf("wakes = %d, want 1 for one batch", n)
	}
}

func TestQueueFull_FailsImmediately(t *testing.T) {
	store := newFakeStore()
	store.readGate = make(chan struct{})
	store.readEntered = make(chan struct{}, 1)
	cfg := testConfig()
	cfg.QueueSize = 1
	a := newTestAllocator(t, store, nil, nil, cfg)

	results := make(chan error, 2)
	go func() {
		_, err := a.Submit(context.Background(), "aws", "r1", []model.Alert{alert("a", "1")})
		results <- err
	}()
	<-store.readEntered // the claimer holds the first submission and is blocked on ReadSeq
	go func() {
		_, err := a.Submit(context.Background(), "aws", "r2", []model.Alert{alert("b", "2")})
		results <- err
	}()
	waitFor(t, func() bool { return len(a.queue) == 1 })

	start := time.Now()
	if _, err := a.Submit(context.Background(), "aws", "r3", []model.Alert{alert("c", "3")}); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("err = %v, want ErrQueueFull", err)
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Error("a full queue must fail immediately, not wait")
	}

	close(store.readGate)
	for range 2 {
		if err := <-results; err != nil {
			t.Errorf("queued submissions should still succeed, got %v", err)
		}
	}
}

func TestShutdown_DrainsQueueThenRejects(t *testing.T) {
	store := newFakeStore()
	store.readGate = make(chan struct{})
	store.readEntered = make(chan struct{}, 1)
	cfg := testConfig()
	cfg.MaxBatch = 1 // the claimer holds exactly one, so the other four stay visibly queued
	a := New(slog.New(slog.NewTextHandler(io.Discard, nil)), store, nil, nil, cfg)

	results := make(chan error, 5)
	for i := range 5 {
		go func() {
			_, err := a.Submit(context.Background(), "aws", "r", []model.Alert{alert("s", strconv.Itoa(i))})
			results <- err
		}()
	}
	<-store.readEntered
	waitFor(t, func() bool { return len(a.queue) == 4 })

	closed := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		closed <- a.Close(ctx)
	}()
	waitFor(t, func() bool { a.mu.RLock(); defer a.mu.RUnlock(); return a.closed })
	if _, err := a.Submit(context.Background(), "aws", "late", []model.Alert{alert("s", "x")}); !errors.Is(err, ErrShuttingDown) {
		t.Errorf("Submit after Close = %v, want ErrShuttingDown", err)
	}

	close(store.readGate)
	for range 5 {
		if err := <-results; err != nil {
			t.Errorf("queued submission lost on shutdown: %v", err)
		}
	}
	if err := <-closed; err != nil {
		t.Errorf("Close = %v", err)
	}
	if len(store.rows) != 5 {
		t.Errorf("rows = %d, want 5", len(store.rows))
	}
}

func TestCancelledRequest_StillWritesItsRow(t *testing.T) {
	store := newFakeStore()
	store.readGate = make(chan struct{})
	a := newTestAllocator(t, store, nil, nil, testConfig())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := a.Submit(ctx, "aws", "r", []model.Alert{alert("s", "u")}); done <- err }()
	waitFor(t, func() bool { return len(a.queue) == 0 })
	cancel()
	if err := <-done; !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}

	close(store.readGate)
	closeCtx, cancelClose := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelClose()
	if err := a.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	if len(store.rows) != 1 {
		t.Errorf("rows = %d, want 1: a claimed id must get its row even after the client left", len(store.rows))
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestCloseTimeout_LogsUnwrittenIDs(t *testing.T) {
	store := newFakeStore()
	store.insertGate = make(chan struct{})
	var logs bytes.Buffer
	var logMu sync.Mutex
	logger := slog.New(slog.NewTextHandler(&lockedWriter{w: &logs, mu: &logMu}, nil))
	a := New(logger, store, nil, nil, testConfig())

	submitted := make(chan struct{})
	go func() {
		_, _ = a.Submit(context.Background(), "aws", "req", []model.Alert{alert("svc", "u1"), alert("svc", "u2")})
		close(submitted)
	}()
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(time.Millisecond) {
		a.pendingMu.Lock()
		n := len(a.pending)
		a.pendingMu.Unlock()
		if n == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("ids were never claimed")
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := a.Close(ctx); err == nil {
		t.Fatal("Close should time out while writes are blocked")
	}
	logMu.Lock()
	out := logs.String()
	logMu.Unlock()
	if !strings.Contains(out, "have no row") || !strings.Contains(out, "ALT000000001") || !strings.Contains(out, "ALT000000002") {
		t.Errorf("log should name the unwritten ids, got:\n%s", out)
	}
	close(store.insertGate)
	<-submitted
}

type lockedWriter struct {
	w  io.Writer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func TestReadBackAlwaysMisses_KeepsStoredAlert(t *testing.T) {
	store := newFakeStore()
	store.neverVisible = true
	notifier := &recordingNotifier{}
	cfg := testConfig()
	cfg.ReadBack = true
	a := newTestAllocator(t, store, notifier, nil, cfg)

	ids, err := a.Submit(context.Background(), "aws", "req", []model.Alert{alert("svc", "u")})
	if err != nil {
		t.Fatalf("err = %v, want success: the alert did land", err)
	}
	if row := store.rows[ids[0]]; !strings.Contains(row.alert, `"service":"svc"`) {
		t.Errorf("row = %q, want the real alert, not a filler", row.alert)
	}
	if len(notifier.failures) != 0 {
		t.Errorf("failures = %+v, want none", notifier.failures)
	}
}
