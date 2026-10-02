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

package engine

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"alert-core-service/internal/model"
	"alert-core-service/internal/store"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeAlerts is a minimal in-memory alertReader; errByID forces a specific error for an id not present in byID.
type fakeAlerts struct {
	byID    map[string]model.Alert
	errByID map[string]error
}

func (f *fakeAlerts) Get(_ context.Context, id string) (model.Alert, error) {
	a, ok := f.byID[id]
	if !ok {
		if err, ok := f.errByID[id]; ok {
			return model.Alert{}, err
		}
		return model.Alert{}, context.DeadlineExceeded // a generic read error, distinct from "not visible yet"
	}
	return a, nil
}

// fakeIncidents is an in-memory incidentStore, safe for concurrent use, mirroring store.IncidentRepo closely enough to exercise the engine's own logic.
type fakeIncidents struct {
	mu   sync.Mutex
	byFP map[string]model.Incident
	// recordCSMIncidentErr forces RecordCSMIncident to fail, simulating a persist failure right after CSM accepted the incident.
	recordCSMIncidentErr error
	// dedupWindow mirrors store.IncidentRepo's own field; defaults generous so existing tests are unaffected unless they shrink it.
	dedupWindow time.Duration
}

func newFakeIncidents() *fakeIncidents {
	return &fakeIncidents{byFP: make(map[string]model.Incident), dedupWindow: time.Hour}
}

func (f *fakeIncidents) FindByFingerprint(_ context.Context, fp string) (model.Incident, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	inc, ok := f.byFP[fp]
	return inc, ok, nil
}

func (f *fakeIncidents) Upsert(_ context.Context, alertID string, a model.Alert, severityNum int) (model.Incident, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fp := model.Fingerprint(a.Source, a.Service, a.MetricName, a.Environment, a.UniqueIdentifier)
	existing, found := f.byFP[fp]
	if !found {
		now := time.Now()
		impact, urgency := model.ImpactUrgency(severityNum)
		inc := model.Incident{
			Fingerprint:    fp,
			IncidentNumber: "PENDING-" + fp[:8],
			Status:         "new",
			Severity:       severityNum,
			Impact:         impact,
			Urgency:        urgency,
			Service:        a.Service,
			MetricName:     a.MetricName,
			AlertIDs:       []string{alertID},
			AlertCount:     1,
			FirstSeen:      now,
			LastSeen:       now,
		}
		f.byFP[fp] = inc
		return inc, true, nil
	}
	for _, seen := range existing.AlertIDs {
		if seen == alertID {
			return existing, false, nil
		}
	}
	existing.AlertIDs = append(existing.AlertIDs, alertID)
	existing.AlertCount++
	if severityNum < existing.Severity {
		existing.Severity = severityNum
	}
	existing.LastSeen = time.Now()
	// Mirrors store.IncidentRepo.Upsert: this path only reaches a no-longer-open incident, so folding a new occurrence in must reset delivery state.
	if !existing.IsOpen(time.Now(), f.dedupWindow) {
		existing.FirstSeen = existing.LastSeen
		existing.IncidentID = ""
		existing.IncidentNumber = "PENDING-" + fp[:8]
		existing.Fallback = false
		existing.CSMConfirmed = false
		existing.CSMAttempts = 0
		existing.CSMPermanentlyFailed = false
		existing.PendingNotes = nil
		existing.StateCheckedAt = time.Time{}
	}
	existing.Version++
	f.byFP[fp] = existing
	return existing, false, nil
}

// casUpdate mirrors store.IncidentRepo.casUpdate's contract: mutate only if existing.Version matches what's stored, else report store.ErrStaleWrite; callers must hold f.mu.
func (f *fakeIncidents) casUpdate(existing model.Incident, mutate func(*model.Incident)) (int64, error) {
	inc, ok := f.byFP[existing.Fingerprint]
	if !ok || inc.Version != existing.Version {
		return existing.Version, store.ErrStaleWrite
	}
	mutate(&inc)
	inc.Version++
	f.byFP[existing.Fingerprint] = inc
	return inc.Version, nil
}

func (f *fakeIncidents) RecordAlertID(_ context.Context, existing model.Incident, alertID string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, seen := range existing.AlertIDs {
		if seen == alertID {
			return existing.Version, nil
		}
	}
	return f.casUpdate(existing, func(inc *model.Incident) {
		inc.AlertIDs = append(inc.AlertIDs, alertID)
	})
}

func (f *fakeIncidents) RecordCSMIncident(_ context.Context, existing model.Incident, incidentID, incidentNumber string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.recordCSMIncidentErr != nil {
		return existing.Version, f.recordCSMIncidentErr
	}
	return f.casUpdate(existing, func(inc *model.Incident) {
		inc.IncidentID = incidentID
		inc.IncidentNumber = incidentNumber
		inc.CSMConfirmed = true
	})
}

func (f *fakeIncidents) RecordCSMAttemptStarted(_ context.Context, existing model.Incident, attempts int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.casUpdate(existing, func(inc *model.Incident) {
		inc.CSMAttempts = attempts
		inc.CSMLastAttemptAt = time.Now()
	})
}

func (f *fakeIncidents) RecordCSMAttemptFailure(_ context.Context, existing model.Incident, attempts, maxAttempts int, permanent bool) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.casUpdate(existing, func(inc *model.Incident) {
		inc.CSMAttempts = attempts
		inc.CSMPermanentlyFailed = permanent || attempts >= maxAttempts
	})
}

func (f *fakeIncidents) SyncStatus(_ context.Context, existing model.Incident, status string, checkedAt time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.casUpdate(existing, func(inc *model.Incident) {
		inc.Status = status
		inc.StateCheckedAt = checkedAt
	})
}

func (f *fakeIncidents) RecordStateChecked(_ context.Context, existing model.Incident, checkedAt time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.casUpdate(existing, func(inc *model.Incident) {
		inc.StateCheckedAt = checkedAt
	})
}

func (f *fakeIncidents) AppendWorkNote(_ context.Context, existing model.Incident, note string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.casUpdate(existing, func(inc *model.Incident) {
		inc.WorkNotes = append(inc.WorkNotes, note)
		inc.PendingNotes = append(inc.PendingNotes, note)
	})
}

func (f *fakeIncidents) ClearPendingNotes(_ context.Context, existing model.Incident, remaining []string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.casUpdate(existing, func(inc *model.Incident) {
		inc.PendingNotes = remaining
	})
}

func (f *fakeIncidents) MarkFallbackNotified(_ context.Context, existing model.Incident) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.casUpdate(existing, func(inc *model.Incident) {
		inc.Fallback = true
	})
}

func (f *fakeIncidents) ListPending(_ context.Context) ([]model.Incident, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []model.Incident
	for _, inc := range f.byFP {
		owesCSMOrChat := !inc.CSMConfirmed && !inc.CSMPermanentlyFailed
		owesNotes := inc.CSMConfirmed && len(inc.PendingNotes) > 0
		if owesCSMOrChat || owesNotes {
			out = append(out, inc)
		}
	}
	return out, nil
}

// fakeNotifier counts NotifyCSM calls so tests can assert no double-delivery, and lets tests control whether CSM confirms.
type fakeNotifier struct {
	csmOK      bool
	csmID      string
	csmNumber  string
	chatOK     bool
	openStates map[string]bool // incident number -> open

	mu            sync.Mutex
	csmCalls      atomic.Int32
	csmAttemptsAt []int // CSMAttempts observed on each NotifyCSM call, in order
	chatCalls     atomic.Int32
	pushNoteCalls atomic.Int32
	pushNoteErr   error
	pushedNotes   []string

	chatAnnotationCalls atomic.Int32
	chatAnnotationKinds []string // kind observed on each NotifyChatAnnotation call, in order
	chatAnnotationNotes []string // note observed on each NotifyChatAnnotation call, in order
}

func (n *fakeNotifier) NotifyCSM(_ context.Context, inc model.Incident) (string, string, bool, bool) {
	n.csmCalls.Add(1)
	n.mu.Lock()
	n.csmAttemptsAt = append(n.csmAttemptsAt, inc.CSMAttempts)
	n.mu.Unlock()
	if !n.csmOK {
		return "", "", false, false
	}
	return n.csmID, n.csmNumber, true, false
}

func (n *fakeNotifier) NotifyChat(_ context.Context, inc model.Incident) bool {
	n.chatCalls.Add(1)
	return n.chatOK
}

func (n *fakeNotifier) NotifyChatAnnotation(_ context.Context, inc model.Incident, kind, note string) bool {
	n.chatAnnotationCalls.Add(1)
	n.mu.Lock()
	n.chatAnnotationKinds = append(n.chatAnnotationKinds, kind)
	n.chatAnnotationNotes = append(n.chatAnnotationNotes, note)
	n.mu.Unlock()
	return n.chatOK
}

func (n *fakeNotifier) PushWorkNote(_ context.Context, incidentID, note string) error {
	n.pushNoteCalls.Add(1)
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.pushNoteErr != nil {
		return n.pushNoteErr
	}
	n.pushedNotes = append(n.pushedNotes, note)
	return nil
}

func (n *fakeNotifier) IncidentState(_ context.Context, incidentNumber string) (bool, bool, error) {
	open, found := n.openStates[incidentNumber]
	return open, found, nil
}

func newTestEngine(alerts map[string]model.Alert, notifier *fakeNotifier) (*Engine, *fakeIncidents) {
	incidents := newFakeIncidents()
	e := New(testLogger(), &fakeAlerts{byID: alerts}, incidents, notifier, model.Defaults{}, 3, 0, time.Hour, CSMRetryConfig{BaseDelay: time.Hour, Multiplier: 3, MaxDelay: time.Hour}, true)
	return e, incidents
}

func TestHandle_ResolvingAlertWithNoMatchingIncident_Ignored(t *testing.T) {
	notifier := &fakeNotifier{}
	e, incidents := newTestEngine(nil, notifier)

	alert := model.Alert{Service: "svc", MetricName: "cpu", Severity: "ok", Source: "vendor"}
	outcome := e.Handle(context.Background(), "ALT1", alert)

	if outcome != Processed {
		t.Fatalf("outcome = %v, want Processed", outcome)
	}
	if len(incidents.byFP) != 0 {
		t.Fatalf("expected no incident to be created for a resolving alert with nothing to resolve, got %d", len(incidents.byFP))
	}
	if notifier.csmCalls.Load() != 0 {
		t.Fatalf("expected NotifyCSM never called for a resolving alert with nothing to resolve")
	}
}

func TestHandle_NewAlertCreatesAndDeliversIncident(t *testing.T) {
	notifier := &fakeNotifier{csmOK: true, csmID: "csm-1", csmNumber: "INC0000001"}
	e, incidents := newTestEngine(nil, notifier)

	alert := model.Alert{Service: "svc", MetricName: "cpu", Severity: "critical", Source: "vendor"}
	outcome := e.Handle(context.Background(), "ALT1", alert)

	if outcome != Processed {
		t.Fatalf("outcome = %v, want Processed", outcome)
	}
	if notifier.csmCalls.Load() != 1 {
		t.Fatalf("NotifyCSM calls = %d, want 1", notifier.csmCalls.Load())
	}
	fp := model.Fingerprint(alert.Source, alert.Service, alert.MetricName, alert.Environment, alert.UniqueIdentifier)
	inc := incidents.byFP[fp]
	if !inc.CSMConfirmed || inc.IncidentNumber != "INC0000001" || inc.IncidentID != "csm-1" {
		t.Fatalf("incident not recorded as csm-confirmed with the real id/number: %+v", inc)
	}
}

func TestHandle_DuplicateAlertOnOpenIncident_Annotates(t *testing.T) {
	notifier := &fakeNotifier{csmOK: true, csmID: "csm-1", csmNumber: "INC0000001"}
	e, incidents := newTestEngine(nil, notifier)

	alert := model.Alert{Service: "svc", MetricName: "cpu", Severity: "critical", Source: "vendor"}
	ctx := context.Background()
	e.Handle(ctx, "ALT1", alert) // creates + confirms the incident

	outcome := e.Handle(ctx, "ALT2", alert)
	if outcome != Processed {
		t.Fatalf("outcome = %v, want Processed", outcome)
	}

	fp := model.Fingerprint(alert.Source, alert.Service, alert.MetricName, alert.Environment, alert.UniqueIdentifier)
	inc := incidents.byFP[fp]
	if len(inc.WorkNotes) != 1 {
		t.Fatalf("expected exactly one work note recorded for the duplicate, got %d: %v", len(inc.WorkNotes), inc.WorkNotes)
	}
	if notifier.csmCalls.Load() != 1 {
		t.Fatalf("NotifyCSM calls = %d, want 1 (a duplicate on an open incident must never re-create it)", notifier.csmCalls.Load())
	}
}

func TestHandle_IdempotentReplaySkipsDuplicateNote(t *testing.T) {
	notifier := &fakeNotifier{csmOK: true, csmID: "csm-1", csmNumber: "INC0000001"}
	e, incidents := newTestEngine(nil, notifier)
	ctx := context.Background()

	alert := model.Alert{Service: "svc", MetricName: "cpu", Severity: "critical", Source: "vendor"}
	e.Handle(ctx, "ALT1", alert) // creates the incident
	e.Handle(ctx, "ALT2", alert) // first delivery of the duplicate note
	e.Handle(ctx, "ALT2", alert) // replayed -- must be a no-op

	fp := model.Fingerprint(alert.Source, alert.Service, alert.MetricName, alert.Environment, alert.UniqueIdentifier)
	inc := incidents.byFP[fp]
	if len(inc.WorkNotes) != 1 {
		t.Fatalf("expected exactly one work note after a replayed alert id, got %d: %v", len(inc.WorkNotes), inc.WorkNotes)
	}
}

func TestRetrySweepAndHandle_NeverDoubleDeliverSameIncident(t *testing.T) {
	notifier := &fakeNotifier{csmOK: true, csmID: "csm-1", csmNumber: "INC0000001"}
	e, incidents := newTestEngine(nil, notifier)
	ctx := context.Background()

	fp := model.Fingerprint("vendor", "svc", "cpu", "", "")
	incidents.byFP[fp] = model.Incident{
		Fingerprint:    fp,
		IncidentNumber: "PENDING-abc",
		Status:         "new",
		Severity:       1,
		Service:        "svc",
		MetricName:     "cpu",
		Source:         "vendor",
		AlertIDs:       []string{"ALT1"},
		AlertCount:     1,
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		e.deliverAndPersist(ctx, fp)
	}()
	go func() {
		defer wg.Done()
		e.deliverAndPersist(ctx, fp)
	}()
	wg.Wait()

	if calls := notifier.csmCalls.Load(); calls != 1 {
		t.Fatalf("NotifyCSM calls = %d, want exactly 1 across two concurrent deliverAndPersist calls for the same fingerprint", calls)
	}
}

func TestHandle_RecurrenceAfterCsmCloses_NotifiesAgain(t *testing.T) {
	notifier := &fakeNotifier{csmOK: true, csmID: "csm-1", csmNumber: "INC0000001", openStates: map[string]bool{}}
	e, incidents := newTestEngine(nil, notifier)
	ctx := context.Background()

	alert := model.Alert{Service: "svc", MetricName: "cpu", Severity: "critical", Source: "vendor"}
	e.Handle(ctx, "ALT1", alert) // creates + confirms the incident
	if notifier.csmCalls.Load() != 1 {
		t.Fatalf("NotifyCSM calls = %d, want 1 after initial creation", notifier.csmCalls.Load())
	}

	notifier.openStates["INC0000001"] = false // CSM has since closed the incident

	notifier.csmNumber = "INC0000002" // the recurrence's own, distinct CSM incident
	outcome := e.Handle(ctx, "ALT2", alert)
	if outcome != Processed {
		t.Fatalf("outcome = %v, want Processed", outcome)
	}
	if calls := notifier.csmCalls.Load(); calls != 2 {
		t.Fatalf("NotifyCSM calls = %d, want 2: a recurrence after CSM closes the incident must be notified again", calls)
	}

	fp := model.Fingerprint(alert.Source, alert.Service, alert.MetricName, alert.Environment, alert.UniqueIdentifier)
	inc := incidents.byFP[fp]
	if inc.IncidentNumber != "INC0000002" || !inc.CSMConfirmed {
		t.Fatalf("expected the recurrence to be recorded against its own new CSM incident, got %+v", inc)
	}
}

func TestHandle_PermanentlyFailedIncident_RecoversOnNextAlert(t *testing.T) {
	notifier := &fakeNotifier{csmOK: false, chatOK: true}
	incidents := newFakeIncidents()
	// maxCSMAttempts=1 so the very first failed attempt already exhausts retries and marks permanent failure.
	e := New(testLogger(), &fakeAlerts{}, incidents, notifier, model.Defaults{}, 1, 0, time.Hour, CSMRetryConfig{BaseDelay: time.Hour, Multiplier: 3, MaxDelay: time.Hour}, true)
	ctx := context.Background()

	alert := model.Alert{Service: "svc", MetricName: "cpu", Severity: "critical", Source: "vendor"}
	e.Handle(ctx, "ALT1", alert) // CSM create fails, exhausts the single attempt, falls back to Chat

	fp := model.Fingerprint(alert.Source, alert.Service, alert.MetricName, alert.Environment, alert.UniqueIdentifier)
	inc := incidents.byFP[fp]
	if !inc.CSMPermanentlyFailed || inc.CSMConfirmed {
		t.Fatalf("expected the incident to be marked permanently failed after exhausting attempts, got %+v", inc)
	}
	if inc.IsOpen(time.Now(), time.Hour) {
		t.Fatalf("expected a permanently-failed incident to report IsOpen()=false")
	}

	// CSM recovers; a later alert on the same fingerprint must get its own fresh delivery attempt, not be swallowed as a Duplicate note forever.
	notifier.csmOK = true
	notifier.csmID, notifier.csmNumber = "csm-2", "INC0000002"
	outcome := e.Handle(ctx, "ALT2", alert)
	if outcome != Processed {
		t.Fatalf("outcome = %v, want Processed", outcome)
	}
	if calls := notifier.csmCalls.Load(); calls != 2 {
		t.Fatalf("NotifyCSM calls = %d, want 2: a permanently-failed incident's recurrence must be retried, not swallowed", calls)
	}

	inc = incidents.byFP[fp]
	if !inc.CSMConfirmed || inc.CSMPermanentlyFailed || inc.IncidentNumber != "INC0000002" {
		t.Fatalf("expected the recurrence to be confirmed against a fresh CSM incident, got %+v", inc)
	}
}

func TestDeliverAndPersist_CSMRetryBacksOffDuringOutage(t *testing.T) {
	notifier := &fakeNotifier{csmOK: false, chatOK: true}
	incidents := newFakeIncidents()
	e := New(testLogger(), &fakeAlerts{}, incidents, notifier, model.Defaults{}, 20, 0, time.Hour, CSMRetryConfig{BaseDelay: 30 * time.Second, Multiplier: 3, MaxDelay: time.Hour}, true)
	ctx := context.Background()

	alert := model.Alert{Service: "svc", MetricName: "cpu", Severity: "critical", Source: "vendor"}
	e.Handle(ctx, "ALT1", alert)
	fp := model.Fingerprint(alert.Source, alert.Service, alert.MetricName, alert.Environment, alert.UniqueIdentifier)
	if calls := notifier.csmCalls.Load(); calls != 1 {
		t.Fatalf("csmCalls = %d, want 1", calls)
	}

	e.deliverAndPersist(ctx, fp)
	if calls := notifier.csmCalls.Load(); calls != 1 {
		t.Fatalf("csmCalls = %d, want still 1: backoff window hasn't elapsed", calls)
	}

	inc := incidents.byFP[fp]
	inc.CSMLastAttemptAt = time.Now().Add(-31 * time.Second)
	incidents.byFP[fp] = inc
	e.deliverAndPersist(ctx, fp)
	if calls := notifier.csmCalls.Load(); calls != 2 {
		t.Fatalf("csmCalls = %d, want 2: backoff window elapsed", calls)
	}
}

func TestHandle_DuplicateWithinDedupWindow_Folds(t *testing.T) {
	notifier := &fakeNotifier{csmOK: true, csmID: "csm-1", csmNumber: "INC0000001"}
	incidents := newFakeIncidents()
	incidents.dedupWindow = 5 * time.Minute
	e := New(testLogger(), &fakeAlerts{}, incidents, notifier, model.Defaults{}, 3, 0, 5*time.Minute, CSMRetryConfig{BaseDelay: time.Hour, Multiplier: 3, MaxDelay: time.Hour}, true)
	ctx := context.Background()

	alert := model.Alert{Service: "svc", MetricName: "cpu", Severity: "critical", Source: "vendor"}
	e.Handle(ctx, "ALT1", alert) // creates + confirms the incident

	fp := model.Fingerprint(alert.Source, alert.Service, alert.MetricName, alert.Environment, alert.UniqueIdentifier)
	inc := incidents.byFP[fp]
	inc.FirstSeen = time.Now().Add(-2 * time.Minute) // 2 minutes into a 5-minute window
	incidents.byFP[fp] = inc

	outcome := e.Handle(ctx, "ALT2", alert)
	if outcome != Processed {
		t.Fatalf("outcome = %v, want Processed", outcome)
	}
	if calls := notifier.csmCalls.Load(); calls != 1 {
		t.Fatalf("NotifyCSM calls = %d, want 1: a duplicate within the dedup window must not open a new incident", calls)
	}
	inc = incidents.byFP[fp]
	if inc.IncidentNumber != "INC0000001" || len(inc.WorkNotes) != 1 {
		t.Fatalf("expected the duplicate to be folded as a work note on the existing incident, got %+v", inc)
	}
}

func TestHandle_DedupWindowExpired_StartsNewIncidentGeneration(t *testing.T) {
	notifier := &fakeNotifier{csmOK: true, csmID: "csm-1", csmNumber: "INC0000001"}
	incidents := newFakeIncidents()
	incidents.dedupWindow = 5 * time.Minute
	e := New(testLogger(), &fakeAlerts{}, incidents, notifier, model.Defaults{}, 3, 0, 5*time.Minute, CSMRetryConfig{BaseDelay: time.Hour, Multiplier: 3, MaxDelay: time.Hour}, true)
	ctx := context.Background()

	alert := model.Alert{Service: "svc", MetricName: "cpu", Severity: "critical", Source: "vendor"}
	e.Handle(ctx, "ALT1", alert) // creates + confirms the incident

	fp := model.Fingerprint(alert.Source, alert.Service, alert.MetricName, alert.Environment, alert.UniqueIdentifier)
	inc := incidents.byFP[fp]
	inc.FirstSeen = time.Now().Add(-6 * time.Minute) // past the 5-minute window
	incidents.byFP[fp] = inc

	notifier.csmID, notifier.csmNumber = "csm-2", "INC0000002"
	outcome := e.Handle(ctx, "ALT2", alert)
	if outcome != Processed {
		t.Fatalf("outcome = %v, want Processed", outcome)
	}
	if calls := notifier.csmCalls.Load(); calls != 2 {
		t.Fatalf("NotifyCSM calls = %d, want 2: an alert past the dedup window must open a fresh incident, not fold as a duplicate", calls)
	}
	inc = incidents.byFP[fp]
	if inc.IncidentNumber != "INC0000002" || !inc.CSMConfirmed || len(inc.WorkNotes) != 0 {
		t.Fatalf("expected a fresh incident generation with no carried-over work notes, got %+v", inc)
	}
}

func TestHandle_GenerationReset_FlushesPendingNotesFirst(t *testing.T) {
	notifier := &fakeNotifier{csmOK: true, csmID: "csm-1", csmNumber: "INC0000001"}
	incidents := newFakeIncidents()
	incidents.dedupWindow = 5 * time.Minute
	e := New(testLogger(), &fakeAlerts{}, incidents, notifier, model.Defaults{}, 3, 0, 5*time.Minute, CSMRetryConfig{BaseDelay: time.Hour, Multiplier: 3, MaxDelay: time.Hour}, true)
	ctx := context.Background()

	alert := model.Alert{Service: "svc", MetricName: "cpu", Severity: "critical", Source: "vendor"}
	e.Handle(ctx, "ALT1", alert) // creates + confirms the incident
	fp := model.Fingerprint(alert.Source, alert.Service, alert.MetricName, alert.Environment, alert.UniqueIdentifier)

	// A note queued on the outgoing generation is still unflushed, and the incident has aged past its dedup window, about to trigger a generation reset.
	inc := incidents.byFP[fp]
	inc.PendingNotes = []string{"queued note"}
	inc.FirstSeen = time.Now().Add(-6 * time.Minute)
	incidents.byFP[fp] = inc

	notifier.csmID, notifier.csmNumber = "csm-2", "INC0000002"
	outcome := e.Handle(ctx, "ALT2", alert)
	if outcome != Processed {
		t.Fatalf("outcome = %v, want Processed", outcome)
	}

	if len(notifier.pushedNotes) != 1 || notifier.pushedNotes[0] != "queued note" {
		t.Fatalf("expected the queued note to be pushed to CSM before the generation reset, got %v", notifier.pushedNotes)
	}
	inc = incidents.byFP[fp]
	if inc.IncidentNumber != "INC0000002" {
		t.Fatalf("expected a fresh incident generation, got %+v", inc)
	}
}

func TestAnnotate_PushesPendingNoteImmediately_AndRetriesOnFailure(t *testing.T) {
	notifier := &fakeNotifier{csmOK: true, csmID: "csm-1", csmNumber: "INC0000001"}
	e, incidents := newTestEngine(nil, notifier)
	ctx := context.Background()

	alert := model.Alert{Service: "svc", MetricName: "cpu", Severity: "critical", Source: "vendor"}
	e.Handle(ctx, "ALT1", alert) // creates + confirms the incident
	fp := model.Fingerprint(alert.Source, alert.Service, alert.MetricName, alert.Environment, alert.UniqueIdentifier)

	// The CSM PATCH fails on this duplicate's note: it must stay pending, not be dropped.
	notifier.pushNoteErr = fmt.Errorf("csm patch failed")
	e.Handle(ctx, "ALT2", alert)
	inc := incidents.byFP[fp]
	if len(inc.PendingNotes) != 1 {
		t.Fatalf("expected the failed push to leave exactly one note pending, got %d: %v", len(inc.PendingNotes), inc.PendingNotes)
	}
	if calls := notifier.pushNoteCalls.Load(); calls != 1 {
		t.Fatalf("PushWorkNote calls = %d, want 1", calls)
	}

	// CSM recovers; RetrySweep must flush the note that was left pending, in order.
	notifier.pushNoteErr = nil
	e.RetrySweep(ctx, func() bool { return true })
	inc = incidents.byFP[fp]
	if len(inc.PendingNotes) != 0 {
		t.Fatalf("expected RetrySweep to flush the pending note, got %d still pending: %v", len(inc.PendingNotes), inc.PendingNotes)
	}
	if len(notifier.pushedNotes) != 1 {
		t.Fatalf("expected exactly one note successfully pushed to csm, got %d: %v", len(notifier.pushedNotes), notifier.pushedNotes)
	}
}

func TestAnnotate_NoteWrittenBeforeCsmConfirmed_FlushesOnceConfirmed(t *testing.T) {
	notifier := &fakeNotifier{csmOK: false} // CSM create keeps failing (transient) while the duplicate arrives
	incidents := newFakeIncidents()
	e := New(testLogger(), &fakeAlerts{}, incidents, notifier, model.Defaults{}, 5, 0, time.Hour, CSMRetryConfig{BaseDelay: 0, Multiplier: 3, MaxDelay: time.Hour}, true)
	ctx := context.Background()

	alert := model.Alert{Service: "svc", MetricName: "cpu", Severity: "critical", Source: "vendor"}
	e.Handle(ctx, "ALT1", alert)            // incident created, but CSM create fails -- still unconfirmed
	outcome := e.Handle(ctx, "ALT2", alert) // duplicate on an incident with no IncidentID yet
	if outcome != Processed {
		t.Fatalf("outcome = %v, want Processed", outcome)
	}

	fp := model.Fingerprint(alert.Source, alert.Service, alert.MetricName, alert.Environment, alert.UniqueIdentifier)
	inc := incidents.byFP[fp]
	if len(inc.PendingNotes) != 1 || inc.CSMConfirmed {
		t.Fatalf("expected the note to be queued pending, unconfirmed, got %+v", inc)
	}
	if calls := notifier.pushNoteCalls.Load(); calls != 0 {
		t.Fatalf("PushWorkNote calls = %d, want 0 before CSM confirms", calls)
	}

	// CSM recovers; the sweep must both confirm the incident and flush the note now that it has an id.
	notifier.csmOK = true
	notifier.csmID, notifier.csmNumber = "csm-1", "INC0000001"
	e.RetrySweep(ctx, func() bool { return true })

	inc = incidents.byFP[fp]
	if !inc.CSMConfirmed {
		t.Fatalf("expected the incident to be csm-confirmed after the sweep, got %+v", inc)
	}
	if len(inc.PendingNotes) != 0 {
		t.Fatalf("expected the pending note to be flushed once CSM confirmed, got %d still pending: %v", len(inc.PendingNotes), inc.PendingNotes)
	}
}

func TestAnnotate_ChatFallbackIncident_ThreadsDuplicateWhenThreadingEnabled(t *testing.T) {
	notifier := &fakeNotifier{csmOK: false, chatOK: true}
	incidents := newFakeIncidents()
	e := New(testLogger(), &fakeAlerts{}, incidents, notifier, model.Defaults{}, 5, 0, time.Hour, CSMRetryConfig{BaseDelay: time.Hour, Multiplier: 3, MaxDelay: time.Hour}, true)
	ctx := context.Background()

	alert := model.Alert{Service: "svc", MetricName: "cpu", Severity: "critical", Source: "vendor"}
	fp := model.Fingerprint(alert.Source, alert.Service, alert.MetricName, alert.Environment, alert.UniqueIdentifier)
	incidents.byFP[fp] = model.Incident{
		Fingerprint: fp, IncidentNumber: "PENDING-abc", Status: "new", Severity: 1,
		Service: "svc", MetricName: "cpu", Source: "vendor", AlertIDs: []string{"ALT1"}, AlertCount: 1,
		FirstSeen: time.Now(), Fallback: true, // already pushed to chat once; CSM never confirmed (IncidentID stays "").
	}

	outcome := e.Handle(ctx, "ALT2", alert) // duplicate on an incident already stuck in chat-fallback
	if outcome != Processed {
		t.Fatalf("outcome = %v, want Processed", outcome)
	}
	if calls := notifier.chatAnnotationCalls.Load(); calls != 1 {
		t.Fatalf("NotifyChatAnnotation calls = %d, want 1: duplicate should thread into the existing fallback message", calls)
	}
	if got, want := notifier.chatAnnotationKinds[0], "Duplicate"; got != want {
		t.Fatalf("annotation kind = %q, want %q: the reply must be visibly labeled, not a repeat of the original card", got, want)
	}
	if !strings.Contains(notifier.chatAnnotationNotes[0], "Duplicate alert received") {
		t.Fatalf("annotation note = %q, want it to name the kind", notifier.chatAnnotationNotes[0])
	}
	if calls := notifier.chatCalls.Load(); calls != 0 {
		t.Fatalf("NotifyChat calls = %d, want 0: a Duplicate/OK annotation must not repost the original fallback card", calls)
	}
}

func TestAnnotate_ChatFallbackIncident_ThreadsOKWhenThreadingEnabled(t *testing.T) {
	notifier := &fakeNotifier{csmOK: false, chatOK: true}
	incidents := newFakeIncidents()
	e := New(testLogger(), &fakeAlerts{}, incidents, notifier, model.Defaults{}, 5, 0, time.Hour, CSMRetryConfig{BaseDelay: time.Hour, Multiplier: 3, MaxDelay: time.Hour}, true)
	ctx := context.Background()

	alert := model.Alert{Service: "svc", MetricName: "cpu", Severity: "ok", Source: "vendor"}
	fp := model.Fingerprint(alert.Source, alert.Service, alert.MetricName, alert.Environment, alert.UniqueIdentifier)
	incidents.byFP[fp] = model.Incident{
		Fingerprint: fp, IncidentNumber: "PENDING-abc", Status: "new", Severity: 1,
		Service: "svc", MetricName: "cpu", Source: "vendor", AlertIDs: []string{"ALT1"}, AlertCount: 1,
		FirstSeen: time.Now(), Fallback: true,
	}

	outcome := e.Handle(ctx, "ALT2", alert) // OK/resolving alert on an incident already stuck in chat-fallback
	if outcome != Processed {
		t.Fatalf("outcome = %v, want Processed", outcome)
	}
	if calls := notifier.chatAnnotationCalls.Load(); calls != 1 {
		t.Fatalf("NotifyChatAnnotation calls = %d, want 1: OK should thread into the existing fallback message", calls)
	}
	if got, want := notifier.chatAnnotationKinds[0], "OK"; got != want {
		t.Fatalf("annotation kind = %q, want %q: the resolution must be visibly labeled, not a repeat of the original critical card", got, want)
	}
	if !strings.Contains(notifier.chatAnnotationNotes[0], "OK alert received") {
		t.Fatalf("annotation note = %q, want it to name the kind", notifier.chatAnnotationNotes[0])
	}
}

func TestAnnotate_ChatFallbackIncident_SkipsThreadingWhenDisabled(t *testing.T) {
	notifier := &fakeNotifier{csmOK: false, chatOK: true}
	incidents := newFakeIncidents()
	e := New(testLogger(), &fakeAlerts{}, incidents, notifier, model.Defaults{}, 5, 0, time.Hour, CSMRetryConfig{BaseDelay: time.Hour, Multiplier: 3, MaxDelay: time.Hour}, false)
	ctx := context.Background()

	alert := model.Alert{Service: "svc", MetricName: "cpu", Severity: "critical", Source: "vendor"}
	fp := model.Fingerprint(alert.Source, alert.Service, alert.MetricName, alert.Environment, alert.UniqueIdentifier)
	incidents.byFP[fp] = model.Incident{
		Fingerprint: fp, IncidentNumber: "PENDING-abc", Status: "new", Severity: 1,
		Service: "svc", MetricName: "cpu", Source: "vendor", AlertIDs: []string{"ALT1"}, AlertCount: 1,
		FirstSeen: time.Now(), Fallback: true,
	}

	outcome := e.Handle(ctx, "ALT2", alert)
	if outcome != Processed {
		t.Fatalf("outcome = %v, want Processed", outcome)
	}
	if calls := notifier.chatAnnotationCalls.Load(); calls != 0 {
		t.Fatalf("NotifyChatAnnotation calls = %d, want 0: threading disabled must preserve today's silent-until-CSM-recovers behavior", calls)
	}
}

func TestDeliverAndPersist_ChatNotSentWhenCsmSucceedsButPersistFails(t *testing.T) {
	notifier := &fakeNotifier{csmOK: true, csmID: "csm-1", csmNumber: "INC0000001", chatOK: true}
	incidents := newFakeIncidents()
	e := New(testLogger(), &fakeAlerts{}, incidents, notifier, model.Defaults{}, 3, 0, time.Hour, CSMRetryConfig{BaseDelay: time.Hour, Multiplier: 3, MaxDelay: time.Hour}, true)
	ctx := context.Background()

	fp := model.Fingerprint("vendor", "svc", "cpu", "", "")
	incidents.byFP[fp] = model.Incident{
		Fingerprint: fp, IncidentNumber: "PENDING-abc", Status: "new",
		Service: "svc", MetricName: "cpu", Source: "vendor", AlertIDs: []string{"ALT1"}, AlertCount: 1,
	}
	incidents.recordCSMIncidentErr = fmt.Errorf("cassandra write failed")

	e.deliverAndPersist(ctx, fp)

	if calls := notifier.chatCalls.Load(); calls != 0 {
		t.Fatalf("NotifyChat calls = %d, want 0: CSM already has the incident even though persisting the result failed", calls)
	}
}

func TestPrepare_DistinguishesNotFoundFromOtherReadErrors(t *testing.T) {
	alerts := &fakeAlerts{
		errByID: map[string]error{
			"NOTFOUND": store.ErrAlertNotFound,
			"DBERR":    context.DeadlineExceeded,
		},
	}
	e := New(testLogger(), alerts, newFakeIncidents(), &fakeNotifier{}, model.Defaults{}, 3, 0, time.Hour, CSMRetryConfig{BaseDelay: time.Hour, Multiplier: 3, MaxDelay: time.Hour}, true)
	ctx := context.Background()

	if _, _, outcome, ready, notFound := e.Prepare(ctx, "NOTFOUND"); ready || outcome != Retry || !notFound {
		t.Fatalf("Prepare(NOTFOUND) = outcome=%v ready=%v notFound=%v, want Retry/false/true", outcome, ready, notFound)
	}
	if _, _, outcome, ready, notFound := e.Prepare(ctx, "DBERR"); ready || outcome != Retry || notFound {
		t.Fatalf("Prepare(DBERR) = outcome=%v ready=%v notFound=%v, want Retry/false/false: a real read error must never be treated as the gap-timeout-eligible case", outcome, ready, notFound)
	}
}

func TestAnnotate_UsesFreshReadNotStaleSnapshot(t *testing.T) {
	// Push fails so the newly appended note stays visible in PendingNotes instead of being flushed away by annotate's deliverAndPersist call.
	notifier := &fakeNotifier{csmOK: true, csmID: "csm-1", csmNumber: "INC0000001", pushNoteErr: fmt.Errorf("csm patch failed")}
	e, incidents := newTestEngine(nil, notifier)
	ctx := context.Background()
	fp := model.Fingerprint("vendor", "svc", "cpu", "", "")

	stale := model.Incident{
		Fingerprint: fp, IncidentID: "csm-1", IncidentNumber: "INC0000001", CSMConfirmed: true,
		PendingNotes: []string{"OLD (already flushed concurrently)"},
	}
	incidents.byFP[fp] = stale
	// Simulate a concurrent RetrySweep flush completing between Handle's read and this annotate call.
	current := stale
	current.PendingNotes = nil
	incidents.byFP[fp] = current

	e.annotate(ctx, stale, "ALT2", "Duplicate", model.Alert{Service: "svc", MetricName: "cpu", Source: "vendor"})

	inc := incidents.byFP[fp]
	if len(inc.PendingNotes) != 1 {
		t.Fatalf("expected annotate to append to the fresh (already-flushed) state, got %d pending: %v", len(inc.PendingNotes), inc.PendingNotes)
	}
	if inc.PendingNotes[0] == "OLD (already flushed concurrently)" {
		t.Fatalf("annotate resurrected a note a concurrent flush had already cleared: %v", inc.PendingNotes)
	}
}

func TestDeliverAndPersist_CSMAttemptsAdvanceEvenWhenConfirmPersistFails(t *testing.T) {
	notifier := &fakeNotifier{csmOK: true, csmID: "csm-1", csmNumber: "INC0000001"}
	incidents := newFakeIncidents()
	incidents.recordCSMIncidentErr = fmt.Errorf("cassandra write failed")
	e := New(testLogger(), &fakeAlerts{}, incidents, notifier, model.Defaults{}, 5, 0, time.Hour, CSMRetryConfig{BaseDelay: 0, Multiplier: 3, MaxDelay: time.Hour}, true)
	ctx := context.Background()

	fp := model.Fingerprint("vendor", "svc", "cpu", "", "")
	incidents.byFP[fp] = model.Incident{
		Fingerprint: fp, IncidentNumber: "PENDING-abc", Status: "new",
		Service: "svc", MetricName: "cpu", Source: "vendor", AlertIDs: []string{"ALT1"}, AlertCount: 1,
	}

	e.deliverAndPersist(ctx, fp) // CSM "accepts" but persisting the confirmation fails
	e.deliverAndPersist(ctx, fp) // a second sweep must not look like the first attempt again

	if got := notifier.csmAttemptsAt; len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("CSMAttempts observed by NotifyCSM = %v, want [1 2]: attempts must advance even when confirming success fails to persist", got)
	}
	inc := incidents.byFP[fp]
	if inc.CSMConfirmed {
		t.Fatalf("expected the incident to remain unconfirmed since RecordCSMIncident always fails, got %+v", inc)
	}
	if inc.CSMAttempts < 2 {
		t.Fatalf("expected CSMAttempts to have advanced past the first attempt, got %d", inc.CSMAttempts)
	}
}
