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

// Package engine dedups alerts by fingerprint and forwards them to CSM and Chat.
package engine

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"time"

	"alert-core-service/internal/model"
	"alert-core-service/internal/store"
)

// alertReader lets tests fake *store.AlertRepo without the full repo type.
type alertReader interface {
	Get(ctx context.Context, id string) (model.Alert, error)
}

// incidentStore lets tests fake *store.IncidentRepo without the full repo type; every mutating method returns the row's new version or store.ErrStaleWrite when another replica wrote first.
type incidentStore interface {
	FindByFingerprint(ctx context.Context, fingerprint string) (model.Incident, bool, error)
	Upsert(ctx context.Context, alertID string, a model.Alert, severityNum int) (model.Incident, bool, error)
	RecordAlertID(ctx context.Context, existing model.Incident, alertID string) (int64, error)
	RecordCSMIncident(ctx context.Context, existing model.Incident, incidentID, incidentNumber string) (int64, error)
	RecordCSMAttemptStarted(ctx context.Context, existing model.Incident, attempts int) (int64, error)
	RecordCSMAttemptFailure(ctx context.Context, existing model.Incident, attempts, maxAttempts int, permanent bool) (int64, error)
	SyncStatus(ctx context.Context, existing model.Incident, status string, checkedAt time.Time) (int64, error)
	RecordStateChecked(ctx context.Context, existing model.Incident, checkedAt time.Time) (int64, error)
	AppendWorkNote(ctx context.Context, existing model.Incident, note string) (int64, error)
	ClearPendingNotes(ctx context.Context, existing model.Incident, remaining []string) (int64, error)
	MarkFallbackNotified(ctx context.Context, existing model.Incident) (int64, error)
	ListPending(ctx context.Context) ([]model.Incident, error)
}

// notifier lets tests fake *notify.Notifier without the full notifier type.
type notifier interface {
	NotifyCSM(ctx context.Context, inc model.Incident) (incidentID, incidentNumber string, ok bool, permanent bool)
	NotifyChat(ctx context.Context, inc model.Incident) (ok bool)
	NotifyChatAnnotation(ctx context.Context, inc model.Incident, kind, note string) (ok bool)
	PushWorkNote(ctx context.Context, incidentID, note string) error
	IncidentState(ctx context.Context, incidentNumber string) (open bool, found bool, err error)
}

// Engine wires one repo per entity plus the notifier together.
type Engine struct {
	logger    *slog.Logger
	alerts    alertReader
	incidents incidentStore
	notifier  notifier
	defaults  model.Defaults
	// maxCSMAttempts caps failed CreateIncident attempts before RetrySweep gives up on the incident.
	maxCSMAttempts int
	// stateCheckInterval throttles CSM searches to at most one per interval during alert flaps.
	stateCheckInterval time.Duration
	// dedupWindow bounds how long an incident absorbs duplicates before starting a new generation.
	dedupWindow time.Duration
	// csmRetry bounds how RetrySweep backs off CSM retries during a prolonged outage.
	csmRetry CSMRetryConfig
	// chatThreadingEnabled gates forwarding Duplicate/OK annotations to Chat (threaded by fingerprint) while an incident is stuck in chat-fallback; see notify.Notifier.chatThreadingEnabled.
	chatThreadingEnabled bool
	// locks is per-fingerprint so distinct incidents never serialize; racing callers re-read the row under lock.
	locks *fpLocks
}

// CSMRetryConfig bounds RetrySweep's exponential backoff for CSM retries: waits grow BaseDelay, BaseDelay*Multiplier, ..., capped at MaxDelay.
type CSMRetryConfig struct {
	BaseDelay  time.Duration
	Multiplier float64
	MaxDelay   time.Duration
}

// New wires the engine's collaborators, alert defaults, CSM attempt cap, state-check throttle, dedup window, CSM retry backoff, and chat threading together.
func New(logger *slog.Logger, alerts alertReader, incidents incidentStore, n notifier, defaults model.Defaults, maxCSMAttempts int, stateCheckInterval time.Duration, dedupWindow time.Duration, csmRetry CSMRetryConfig, chatThreadingEnabled bool) *Engine {
	return &Engine{
		logger: logger, alerts: alerts, incidents: incidents, notifier: n, defaults: defaults,
		maxCSMAttempts: maxCSMAttempts, stateCheckInterval: stateCheckInterval, dedupWindow: dedupWindow,
		csmRetry: csmRetry, chatThreadingEnabled: chatThreadingEnabled, locks: newFPLocks(),
	}
}

// Outcome tells the poller whether it may advance its cursor past an alert id or must retry it.
type Outcome int

const (
	// Processed: the poller may advance its cursor past this alert id.
	Processed Outcome = iota
	// Retry: a transient failure occurred; the poller retries this id next cycle.
	Retry
	// Failed: the alert will never process successfully, so the poller skips it.
	Failed
)

func (o Outcome) String() string {
	switch o {
	case Processed:
		return "processed"
	case Retry:
		return "retry"
	case Failed:
		return "failed"
	default:
		return "unknown"
	}
}

// Process handles one stored alert id end to end via Prepare and Handle.
func (e *Engine) Process(ctx context.Context, alertID string) Outcome {
	alert, _, outcome, ready, _ := e.Prepare(ctx, alertID)
	if !ready {
		return outcome
	}
	return e.Handle(ctx, alertID, alert)
}

// Prepare reads and normalizes an alert; notFound distinguishes transient replication lag from real errors so the poller's gap-timeout skip only fires on the former.
func (e *Engine) Prepare(ctx context.Context, alertID string) (alert model.Alert, fingerprint string, outcome Outcome, ready bool, notFound bool) {
	alert, err := e.alerts.Get(ctx, alertID)
	if err != nil {
		if errors.Is(err, store.ErrMalformedAlert) {
			e.logger.Error("alert unprocessable, skipping", "alert_id", alertID, "error", err)
			return model.Alert{}, "", Failed, false, false
		}
		if errors.Is(err, store.ErrAlertNotFound) {
			e.logger.Info("alert not visible yet, will retry", "alert_id", alertID, "error", err)
			return model.Alert{}, "", Retry, false, true
		}
		e.logger.Warn("alert read failed, will retry", "alert_id", alertID, "error", err)
		return model.Alert{}, "", Retry, false, false
	}
	e.defaults.Apply(&alert)
	fingerprint = model.Fingerprint(alert.Source, alert.Service, alert.MetricName, alert.Environment, alert.UniqueIdentifier)
	return alert, fingerprint, Processed, true, false
}

// Handle folds a normalized alert into its incident; concurrent calls must use distinct fingerprints.
func (e *Engine) Handle(ctx context.Context, alertID string, alert model.Alert) Outcome {
	severityNum, recognized := model.SeverityToNumeric(alert.Severity)
	if !recognized {
		// Log loudly: a silent default to Critical could page people for a typo.
		e.logger.Warn("unrecognized severity label, defaulting to critical", "alert_id", alertID, "severity", alert.Severity)
	}
	fp := model.Fingerprint(alert.Source, alert.Service, alert.MetricName, alert.Environment, alert.UniqueIdentifier)

	existing, found, err := e.incidents.FindByFingerprint(ctx, fp)
	if err != nil {
		e.logger.Warn("incident lookup failed, will retry", "alert_id", alertID, "error", err)
		return Retry
	}

	if model.IsResolving(severityNum) && !found {
		// Nothing to annotate/fold; Upsert would spuriously create an incident from an OK/Clear alert.
		e.logger.Info("resolving alert with no matching incident, ignoring", "alert_id", alertID, "fingerprint", fp)
		return Processed
	}

	if found {
		// Only CSM authoritatively closes incidents, so refresh local state before deciding.
		existing = e.syncIncidentState(ctx, existing)

		// Replayed poll windows re-run already-handled ids; AlertIDs makes that a no-op here.
		if slices.Contains(existing.AlertIDs, alertID) {
			e.logger.Info("alert id already recorded on this incident, skipping duplicate replay", "incident_number", existing.IncidentNumber, "alert_id", alertID)
			return Processed
		}

		// Must run before Upsert, or Clear's severity value would poison the incident update.
		if model.IsResolving(severityNum) {
			return e.annotate(ctx, existing, alertID, "OK", alert)
		}

		// Duplicate against an open incident is annotated; against a closed one it falls through to Upsert.
		if existing.IsOpen(time.Now(), e.dedupWindow) {
			return e.annotate(ctx, existing, alertID, "Duplicate", alert)
		}

		// Flush notes owed to the old CSM incident before Upsert resets to a new generation.
		e.flushBeforeGenerationReset(ctx, existing)
	}

	inc, isNew, err := e.incidents.Upsert(ctx, alertID, alert, severityNum)
	if err != nil {
		e.logger.Warn("incident upsert failed, will retry", "alert_id", alertID, "error", err)
		return Retry
	}

	if isNew {
		e.logger.Info("incident created", "incident_number", inc.IncidentNumber, "alert_id", alertID,
			"service", inc.Service, "metric_name", inc.MetricName, "severity", inc.Severity)
	} else {
		e.logger.Info("incident updated", "incident_number", inc.IncidentNumber, "alert_id", alertID,
			"alert_count", inc.AlertCount)
	}

	// Delivery is decoupled from processing; RetrySweep retries any pending delivery later.
	e.deliverAndPersist(ctx, inc.Fingerprint)
	return Processed
}

// annotate appends a work note and records alertID for idempotency; the lock prevents concurrent PendingNotes updates.
func (e *Engine) annotate(ctx context.Context, existing model.Incident, alertID, kind string, alert model.Alert) Outcome {
	fp := existing.Fingerprint
	unlock := e.locks.lock(fp)

	current, found, err := e.incidents.FindByFingerprint(ctx, fp)
	if err != nil {
		unlock()
		e.logger.Warn("incident re-read failed, will retry", "alert_id", alertID, "error", err)
		return Retry
	}
	if !found {
		unlock()
		e.logger.Warn("incident vanished before annotate", "fingerprint", fp, "alert_id", alertID)
		return Retry
	}

	note := model.BuildWorkNote(kind, alertID, alert.MetricName, alert.Source)
	newVersion, err := e.incidents.AppendWorkNote(ctx, current, note)
	if err != nil {
		unlock()
		if errors.Is(err, store.ErrStaleWrite) {
			e.logger.Warn("incident changed concurrently, will retry", "alert_id", alertID, "fingerprint", fp)
		} else {
			e.logger.Warn("work note append failed, will retry", "alert_id", alertID, "error", err)
		}
		return Retry
	}
	current.Version = newVersion
	if _, err := e.incidents.RecordAlertID(ctx, current, alertID); err != nil {
		// Best-effort: failure just risks a redundant note on a future replay, not a lost alert.
		e.logger.Warn("failed to record alert id for idempotency, continuing", "alert_id", alertID, "error", err)
	}
	incidentID, incidentNumber := current.IncidentID, current.IncidentNumber
	unlock()

	// Push now if CSM already has this incident, else it stays in PendingNotes for RetrySweep; deliverAndPersist re-locks itself, so it must run after unlock.
	if incidentID != "" {
		e.deliverAndPersist(ctx, fp)
	} else if e.chatThreadingEnabled && current.Fallback {
		// CSM never confirmed, but this incident already reached Chat once; thread this Duplicate/OK in as a reply instead of leaving it silent until CSM recovers. Best-effort: the work note above already persisted either way.
		chatText := model.BuildChatAnnotationText(kind, alert.MetricName, alert.Source)
		if !e.notifier.NotifyChatAnnotation(ctx, current, kind, chatText) {
			e.logger.Warn("chat thread reply failed for annotated incident", "incident_number", incidentNumber, "alert_id", alertID, "kind", kind)
		}
	}
	e.logger.Info("alert recorded on existing incident", "incident_number", incidentNumber, "alert_id", alertID, "kind", kind)
	return Processed
}

// syncIncidentState refreshes local Status from CSM and throttles checks to prevent storms of duplicates.
func (e *Engine) syncIncidentState(ctx context.Context, inc model.Incident) model.Incident {
	if !inc.CSMConfirmed {
		return inc // nothing created on CSM yet.
	}
	if e.stateCheckInterval > 0 && time.Since(inc.StateCheckedAt) < e.stateCheckInterval {
		return inc
	}
	open, found, err := e.notifier.IncidentState(ctx, inc.IncidentNumber)
	if err != nil {
		// Don't stamp StateCheckedAt so a failed check doesn't extend the throttle window.
		e.logger.Warn("csm incident state check failed, using last known state", "incident_number", inc.IncidentNumber, "error", err)
		return inc
	}
	now := time.Now()
	status := inc.Status
	if found {
		status = "closed"
		if open {
			status = "open"
		}
	}
	if status == inc.Status {
		newVersion, err := e.incidents.RecordStateChecked(ctx, inc, now)
		if err != nil {
			if !errors.Is(err, store.ErrStaleWrite) {
				e.logger.Warn("failed to persist state check timestamp", "incident_number", inc.IncidentNumber, "error", err)
			}
			return inc
		}
		inc.Version = newVersion
		inc.StateCheckedAt = now
		return inc
	}
	newVersion, err := e.incidents.SyncStatus(ctx, inc, status, now)
	if err != nil {
		if !errors.Is(err, store.ErrStaleWrite) {
			e.logger.Warn("failed to persist synced incident status", "incident_number", inc.IncidentNumber, "error", err)
		}
		return inc
	}
	inc.Version = newVersion
	inc.Status = status
	inc.StateCheckedAt = now
	return inc
}

// persistTimeout bounds recording a delivery result after its external call already completed.
const persistTimeout = 5 * time.Second

// persistCtx survives parent cancellation so results record even on shutdown.
func persistCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
}

// deliverAndPersist re-reads the incident under its fingerprint's lock, delivers what's owed, and persists the result.
func (e *Engine) deliverAndPersist(ctx context.Context, fingerprint string) {
	unlock := e.locks.lock(fingerprint)
	defer unlock()

	inc, found, err := e.incidents.FindByFingerprint(ctx, fingerprint)
	if err != nil {
		e.logger.Error("delivery: failed to re-read incident", "fingerprint", fingerprint, "error", err)
		return
	}
	if !found {
		e.logger.Warn("delivery: incident vanished before delivery", "fingerprint", fingerprint)
		return
	}
	if inc.CSMConfirmed && inc.Fallback && len(inc.PendingNotes) == 0 {
		return // already delivered by a caller that beat us to this lock, and no notes still owed.
	}

	csmConfirmed := inc.CSMConfirmed
	// csmSucceeded tracks actual CSM acceptance independent of persistence; prevents Chat redundancy if CSM succeeded.
	csmSucceeded := csmConfirmed
	if !csmConfirmed && inc.CSMRetryDue(time.Now(), e.csmRetry.BaseDelay, e.csmRetry.Multiplier, e.csmRetry.MaxDelay) {
		// Persist attempt count before NotifyCSM so CSMAttempts is a lower bound on attempts that may have reached CSM.
		attempts := inc.CSMAttempts + 1
		pctx, cancel := persistCtx(ctx)
		newVersion, startErr := e.incidents.RecordCSMAttemptStarted(pctx, inc, attempts)
		cancel()
		if startErr != nil {
			if errors.Is(startErr, store.ErrStaleWrite) {
				// Another replica already wrote this row (e.g. took over leadership); back off instead of notifying CSM without exclusive delivery.
				e.logger.Warn("incident changed concurrently, deferring delivery", "incident_number", inc.IncidentNumber)
				return
			}
			e.logger.Error("failed to record csm attempt start, will retry", "incident_number", inc.IncidentNumber, "error", startErr)
		} else {
			inc.Version = newVersion
			inc.CSMAttempts = attempts
			id, number, ok, permanent := e.notifier.NotifyCSM(ctx, inc)
			if ok {
				csmSucceeded = true
				pctx, cancel := persistCtx(ctx)
				newVersion, err := e.incidents.RecordCSMIncident(pctx, inc, id, number)
				cancel()
				if err != nil {
					// Don't mark confirmed locally; the next attempt's dedup-by-tag search will find this incident instead of duplicating it.
					if errors.Is(err, store.ErrStaleWrite) {
						e.logger.Warn("incident changed concurrently while recording csm confirmation; relying on dedup-by-tag search to avoid duplicating it", "incident_number", inc.IncidentNumber, "csm_incident_id", id, "csm_incident_number", number)
					} else {
						e.logger.Error("failed to persist csm incident, will retry", "incident_number", inc.IncidentNumber, "csm_incident_id", id, "csm_incident_number", number, "error", err)
					}
				} else {
					inc.Version = newVersion
					csmConfirmed = true
					inc.IncidentNumber = number
					inc.IncidentID = id
				}
			} else {
				pctx, cancel := persistCtx(ctx)
				newVersion, err := e.incidents.RecordCSMAttemptFailure(pctx, inc, attempts, e.maxCSMAttempts, permanent)
				cancel()
				if err != nil {
					if !errors.Is(err, store.ErrStaleWrite) {
						e.logger.Error("failed to record csm attempt failure", "incident_number", inc.IncidentNumber, "error", err)
					}
				} else {
					inc.Version = newVersion
					if permanent || attempts >= e.maxCSMAttempts {
						e.logger.Error("csm permanently failed for incident, giving up", "incident_number", inc.IncidentNumber, "attempts", attempts, "permanent", permanent)
					}
				}
			}
		}
	}

	if csmConfirmed && len(inc.PendingNotes) > 0 {
		inc = e.flushPendingNotes(ctx, inc)
	}

	chatNotified := inc.Fallback
	if !csmSucceeded && !chatNotified {
		// Fall back to chat so a human sees it, but only the first time to avoid spamming retries.
		chatNotified = e.notifier.NotifyChat(ctx, inc)
	}
	if chatNotified && !inc.Fallback {
		pctx, cancel := persistCtx(ctx)
		_, err := e.incidents.MarkFallbackNotified(pctx, inc)
		cancel()
		if err != nil && !errors.Is(err, store.ErrStaleWrite) {
			e.logger.Error("failed to persist fallback-notified flag", "incident_number", inc.IncidentNumber, "error", err)
		}
	}
}

// flushPendingNotes pushes notes in order and persists progress even on partial failure.
func (e *Engine) flushPendingNotes(ctx context.Context, inc model.Incident) model.Incident {
	remaining := inc.PendingNotes
	for i, note := range inc.PendingNotes {
		if err := e.notifier.PushWorkNote(ctx, inc.IncidentID, note); err != nil {
			e.logger.Warn("failed to push work note to csm, will retry", "incident_number", inc.IncidentNumber, "error", err)
			remaining = inc.PendingNotes[i:]
			break
		}
		remaining = inc.PendingNotes[i+1:]
	}
	if len(remaining) == len(inc.PendingNotes) {
		return inc // no progress made; nothing to persist.
	}
	pctx, cancel := persistCtx(ctx)
	newVersion, err := e.incidents.ClearPendingNotes(pctx, inc, remaining)
	cancel()
	if err != nil {
		if !errors.Is(err, store.ErrStaleWrite) {
			e.logger.Error("failed to persist pending notes progress", "incident_number", inc.IncidentNumber, "error", err)
		}
		return inc
	}
	inc.Version = newVersion
	inc.PendingNotes = remaining
	return inc
}

// flushBeforeGenerationReset pushes notes to the old CSM incident before Upsert starts a new generation (best-effort).
func (e *Engine) flushBeforeGenerationReset(ctx context.Context, existing model.Incident) {
	if !existing.CSMConfirmed || len(existing.PendingNotes) == 0 {
		return // nothing owed to CSM (unconfirmed incidents have no CSM incident to push a note to).
	}
	fp := existing.Fingerprint
	unlock := e.locks.lock(fp)
	defer unlock()

	current, found, err := e.incidents.FindByFingerprint(ctx, fp)
	if err != nil || !found {
		return // best-effort; Upsert's own re-read proceeds regardless.
	}
	if current.IncidentID != existing.IncidentID || !current.CSMConfirmed || len(current.PendingNotes) == 0 {
		return // already flushed, or generation already changed under us; nothing left to do here.
	}
	e.flushPendingNotes(ctx, current)
}

// RetrySweep retries delivery for every pending incident, stopping early if leadership is lost.
func (e *Engine) RetrySweep(ctx context.Context, stillLeader func() bool) {
	pending, err := e.incidents.ListPending(ctx)
	if err != nil {
		e.logger.Error("notify retry sweep: failed to list pending incidents", "error", err)
		return
	}
	for _, inc := range pending {
		if !stillLeader() {
			e.logger.Warn("lost leadership mid-sweep, stopping", "incident_number", inc.IncidentNumber)
			return
		}
		e.deliverAndPersist(ctx, inc.Fingerprint)
	}
}
