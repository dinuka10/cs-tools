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

package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gocql/gocql"
	"github.com/scylladb/gocqlx/v2"
	"github.com/scylladb/gocqlx/v2/qb"

	"alert-core-service/internal/model"
)

var incidentColumns = []string{
	"fingerprint", "incident_id", "incident_number", "status", "severity", "impact", "urgency", "service",
	"metric_name", "description", "category", "environment", "source", "alert_ids", "alert_count", "work_notes",
	"pending_notes", "first_seen", "last_seen", "state_checked_at", "fallback", "csm_confirmed", "csm_attempts",
	"csm_permanently_failed", "csm_last_attempt_at", "version",
}

// Bounds unbounded lists so a flapping alert can't blow past Cosmos's row-size limit; AlertCount keeps growing regardless.
const maxWorkNotes = 200

// ErrStaleWrite means another replica already wrote this row first (e.g. a new leader after this one's lease expired); callers should drop the write and let the next read pick up fresh state.
var ErrStaleWrite = errors.New("incident row was modified concurrently")

// IncidentRepo owns the incidents table; dedup uniqueness is a lightweight CAS transaction on fingerprint.
type IncidentRepo struct {
	session gocqlx.Session
	// maxAlertIDs bounds AlertIDs for replay idempotency; must be >= poll.max_window to prevent re-duplicating trimmed IDs.
	maxAlertIDs int
	// dedupWindow bounds how long an incident absorbs duplicates before starting a new generation.
	dedupWindow time.Duration
}

// NewIncidentRepo ties the AlertIDs idempotency cap to the poller's own max_window so the two can't drift apart.
func NewIncidentRepo(session *gocql.Session, maxWindow int, dedupWindow time.Duration) (*IncidentRepo, error) {
	return &IncidentRepo{session: gocqlx.NewSession(session), maxAlertIDs: maxWindow, dedupWindow: dedupWindow}, nil
}

// pendingIncidentNumber is a placeholder until CSM assigns the real one, derived from fingerprint so it's deterministic.
func pendingIncidentNumber(fingerprint string) string {
	return "PENDING-" + fingerprint[:12]
}

// capTail keeps only the last max entries of list, so a long-lived, flapping incident's stored history stays bounded.
func capTail[T any](list []T, max int) []T {
	if len(list) <= max {
		return list
	}
	return append([]T{}, list[len(list)-max:]...)
}

// isPending returns true if an incident still owes CSM/Chat delivery.
func isPending(csmConfirmed, csmPermanentlyFailed bool, pendingNotesLen int) bool {
	owesCSMOrChat := !csmConfirmed && !csmPermanentlyFailed
	owesNotes := csmConfirmed && pendingNotesLen > 0
	return owesCSMOrChat || owesNotes
}

// casUpdate applies a version-fenced UPDATE on incidents_processed (setCols/values must exclude "version"/"fingerprint", bound here), returning the new version or ErrStaleWrite on a lost race.
func (r *IncidentRepo) casUpdate(ctx context.Context, fp string, expectedVersion int64, setCols []string, values qb.M) (int64, error) {
	stmt, names := qb.Update("incidents_processed").
		Set(setCols...).
		SetNamed("version", "new_version").
		Where(qb.Eq("fingerprint")).
		If(qb.EqNamed("version", "expected_version")).
		ToCql()
	values["fingerprint"] = fp
	values["new_version"] = expectedVersion + 1
	values["expected_version"] = expectedVersion
	applied, err := r.session.Query(stmt, names).WithContext(ctx).BindMap(values).ExecCASRelease()
	if err != nil {
		return 0, fmt.Errorf("cas update incident %s: %w", fp, err)
	}
	if !applied {
		return 0, fmt.Errorf("cas update incident %s: %w", fp, ErrStaleWrite)
	}
	return expectedVersion + 1, nil
}

// setPendingIndex syncs incidents_pending without re-reading incidents_processed.
func (r *IncidentRepo) setPendingIndex(ctx context.Context, fp string, pending bool) error {
	if pending {
		stmt, names := qb.Insert("incidents_pending").Columns("fingerprint").ToCql()
		if err := r.session.Query(stmt, names).WithContext(ctx).BindMap(qb.M{"fingerprint": fp}).ExecRelease(); err != nil {
			return fmt.Errorf("mark incident %s pending: %w", fp, err)
		}
		return nil
	}
	stmt, names := qb.Delete("incidents_pending").Where(qb.Eq("fingerprint")).ToCql()
	if err := r.session.Query(stmt, names).WithContext(ctx).BindMap(qb.M{"fingerprint": fp}).ExecRelease(); err != nil {
		return fmt.Errorf("clear incident %s from pending index: %w", fp, err)
	}
	return nil
}

// syncPendingIndex re-derives pending status from the ground-truth row without a full-table scan.
func (r *IncidentRepo) syncPendingIndex(ctx context.Context, fp string) error {
	inc, found, err := r.get(ctx, fp)
	if err != nil {
		return fmt.Errorf("sync pending index for %s: %w", fp, err)
	}
	if !found {
		return nil
	}
	return r.setPendingIndex(ctx, fp, isPending(inc.CSMConfirmed, inc.CSMPermanentlyFailed, len(inc.PendingNotes)))
}

// Upsert maps the alert onto an incident by fingerprint, creating it via IF NOT EXISTS or updating it.
func (r *IncidentRepo) Upsert(ctx context.Context, alertID string, a model.Alert, severityNum int) (model.Incident, bool, error) {
	fp := model.Fingerprint(a.Source, a.Service, a.MetricName, a.Environment, a.UniqueIdentifier)

	existing, found, err := r.get(ctx, fp)
	if err != nil {
		return model.Incident{}, false, err
	}

	if !found {
		now := time.Now().UTC()
		impact, urgency := model.ImpactUrgency(severityNum)
		inc := model.Incident{
			Fingerprint:    fp,
			IncidentNumber: pendingIncidentNumber(fp),
			Status:         "new",
			Severity:       severityNum,
			Impact:         impact,
			Urgency:        urgency,
			Service:        a.Service,
			MetricName:     a.MetricName,
			Description:    model.BuildCreationNote(alertID, a),
			Category:       a.Category,
			Environment:    a.Environment,
			Source:         a.Source,
			AlertIDs:       []string{alertID},
			AlertCount:     1,
			FirstSeen:      now,
			LastSeen:       now,
		}
		stmt, names := qb.Insert("incidents_processed").Columns(incidentColumns...).Unique().ToCql()
		applied, err := r.session.Query(stmt, names).WithContext(ctx).BindStruct(inc).ExecCASRelease()
		if err != nil {
			return model.Incident{}, false, fmt.Errorf("create incident %s: %w", fp, err)
		}
		if applied {
			// Best-effort: row is durable, Handle's idempotency check would skip retry, so a missed index write just delays RetrySweep.
			_ = r.setPendingIndex(ctx, fp, true)
			return inc, true, nil
		}
		// Lost the race to another core; fall through and treat this alert as an update.
		existing, found, err = r.get(ctx, fp)
		if err != nil {
			return model.Incident{}, false, err
		}
		if !found {
			ins, insNames := qb.Insert("incidents_processed").Columns(incidentColumns...).ToCql()
			if err := r.session.Query(ins, insNames).WithContext(ctx).BindStruct(inc).ExecRelease(); err != nil {
				return model.Incident{}, false, fmt.Errorf("create incident %s (unconditional after stale CAS): %w", fp, err)
			}
			// Best-effort, same reasoning as the CAS-applied branch above.
			_ = r.setPendingIndex(ctx, fp, true)
			return inc, true, nil
		}
	}

	// Skip if alertID already folded in (retry after ambiguous timeout); engine.Handle's idempotency check relies on this too.
	for _, seen := range existing.AlertIDs {
		if seen == alertID {
			return existing, false, nil
		}
	}

	updated := existing
	updated.AlertIDs = capTail(append(append([]string{}, existing.AlertIDs...), alertID), r.maxAlertIDs)
	updated.AlertCount = existing.AlertCount + 1
	if severityNum < existing.Severity { // lower number = more severe
		updated.Severity = severityNum
		// Impact/Urgency must be recomputed on escalation -- otherwise a Minor->Critical incident keeps its original, now-stale pair.
		updated.Impact, updated.Urgency = model.ImpactUrgency(severityNum)
	}
	updated.LastSeen = time.Now().UTC()
	if updated.Category == "" && a.Category != "" {
		// Self-heal: an incident with no category yet picks one up from a later alert instead of staying blank.
		updated.Category = a.Category
	}
	if updated.Description == "" {
		// Self-heal: same as Category, so an incident created before its first descriptive alert still fills in.
		updated.Description = model.BuildCreationNote(alertID, a)
	}

	setCols := []string{"alert_ids", "alert_count", "severity", "impact", "urgency", "category", "description", "last_seen"}
	values := qb.M{
		"alert_ids":   updated.AlertIDs,
		"alert_count": updated.AlertCount,
		"severity":    updated.Severity,
		"impact":      updated.Impact,
		"urgency":     updated.Urgency,
		"category":    updated.Category,
		"description": updated.Description,
		"last_seen":   updated.LastSeen,
	}
	// Reset delivery fields on generation boundaries to prevent silently swallowing recurrences.
	if !existing.IsOpen(time.Now(), r.dedupWindow) {
		// Rebuild Description with new alert ID so NotifyCSM doesn't push a stale creation note.
		updated.Description = model.BuildCreationNote(alertID, a)
		updated.Status = "new"
		updated.IncidentID = ""
		updated.IncidentNumber = pendingIncidentNumber(fp)
		updated.Fallback = false
		updated.CSMConfirmed = false
		updated.CSMAttempts = 0
		updated.CSMPermanentlyFailed = false
		updated.CSMLastAttemptAt = time.Time{}
		updated.FirstSeen = updated.LastSeen
		updated.PendingNotes = nil
		updated.StateCheckedAt = time.Time{}
		setCols = append(setCols, "status", "incident_id", "incident_number", "fallback", "csm_confirmed", "csm_attempts",
			"csm_permanently_failed", "csm_last_attempt_at", "first_seen", "pending_notes", "state_checked_at")
		values["description"] = updated.Description
		values["status"] = updated.Status
		values["incident_id"] = updated.IncidentID
		values["incident_number"] = updated.IncidentNumber
		values["fallback"] = updated.Fallback
		values["csm_confirmed"] = updated.CSMConfirmed
		values["csm_attempts"] = updated.CSMAttempts
		values["csm_permanently_failed"] = updated.CSMPermanentlyFailed
		values["csm_last_attempt_at"] = updated.CSMLastAttemptAt
		values["first_seen"] = updated.FirstSeen
		values["pending_notes"] = updated.PendingNotes
		values["state_checked_at"] = updated.StateCheckedAt
	}

	newVersion, err := r.casUpdate(ctx, fp, existing.Version, setCols, values)
	if err != nil {
		return model.Incident{}, false, fmt.Errorf("update incident %s: %w", fp, err)
	}
	updated.Version = newVersion
	// Best-effort: row is durable, so a missed index sync doesn't affect Handle's idempotency check.
	_ = r.setPendingIndex(ctx, fp, isPending(updated.CSMConfirmed, updated.CSMPermanentlyFailed, len(updated.PendingNotes)))
	return updated, false, nil
}

// RecordAlertID appends alertID for idempotent annotate-only paths (no-op if already present) and returns the row's new version so callers can chain further fenced writes.
func (r *IncidentRepo) RecordAlertID(ctx context.Context, existing model.Incident, alertID string) (int64, error) {
	for _, seen := range existing.AlertIDs {
		if seen == alertID {
			return existing.Version, nil
		}
	}
	updated := capTail(append(append([]string{}, existing.AlertIDs...), alertID), r.maxAlertIDs)
	newVersion, err := r.casUpdate(ctx, existing.Fingerprint, existing.Version, []string{"alert_ids"}, qb.M{
		"alert_ids": updated,
	})
	if err != nil {
		return existing.Version, fmt.Errorf("record alert id on incident %s: %w", existing.Fingerprint, err)
	}
	return newVersion, nil
}

// RecordCSMIncident writes id, number, and csm_confirmed together so confirmed is never observed with a placeholder id.
func (r *IncidentRepo) RecordCSMIncident(ctx context.Context, existing model.Incident, incidentID, incidentNumber string) (int64, error) {
	newVersion, err := r.casUpdate(ctx, existing.Fingerprint, existing.Version,
		[]string{"incident_id", "incident_number", "csm_confirmed"}, qb.M{
			"incident_id":     incidentID,
			"incident_number": incidentNumber,
			"csm_confirmed":   true,
		})
	if err != nil {
		return existing.Version, fmt.Errorf("record csm incident for %s: %w", existing.Fingerprint, err)
	}
	// Best-effort: csm_confirmed is durable, so a missed index sync mustn't mask CSM success.
	_ = r.syncPendingIndex(ctx, existing.Fingerprint)
	return newVersion, nil
}

// RecordCSMAttemptStarted persists attempts before NotifyCSM so the count is a lower bound for fail-open decisions.
func (r *IncidentRepo) RecordCSMAttemptStarted(ctx context.Context, existing model.Incident, attempts int) (int64, error) {
	newVersion, err := r.casUpdate(ctx, existing.Fingerprint, existing.Version,
		[]string{"csm_attempts", "csm_last_attempt_at"}, qb.M{
			"csm_attempts":        attempts,
			"csm_last_attempt_at": time.Now().UTC(),
		})
	if err != nil {
		return existing.Version, fmt.Errorf("record csm attempt started for %s: %w", existing.Fingerprint, err)
	}
	return newVersion, nil
}

// RecordCSMAttemptFailure sets csm_permanently_failed once attempts are exhausted or CSM rejects non-retryably, stopping RetrySweep from retrying forever.
func (r *IncidentRepo) RecordCSMAttemptFailure(ctx context.Context, existing model.Incident, attempts, maxAttempts int, permanent bool) (int64, error) {
	failed := permanent || attempts >= maxAttempts
	newVersion, err := r.casUpdate(ctx, existing.Fingerprint, existing.Version,
		[]string{"csm_attempts", "csm_permanently_failed"}, qb.M{
			"csm_attempts":           attempts,
			"csm_permanently_failed": failed,
		})
	if err != nil {
		return existing.Version, fmt.Errorf("record csm attempt failure for %s: %w", existing.Fingerprint, err)
	}
	// Best-effort: attempt/failure state is durable, so a missed index sync mustn't mask durability.
	_ = r.syncPendingIndex(ctx, existing.Fingerprint)
	return newVersion, nil
}

// SyncStatus persists CSM's status and checkedAt timestamp for throttling future state checks.
func (r *IncidentRepo) SyncStatus(ctx context.Context, existing model.Incident, status string, checkedAt time.Time) (int64, error) {
	newVersion, err := r.casUpdate(ctx, existing.Fingerprint, existing.Version,
		[]string{"status", "state_checked_at"}, qb.M{
			"status":           status,
			"state_checked_at": checkedAt,
		})
	if err != nil {
		return existing.Version, fmt.Errorf("sync status for %s: %w", existing.Fingerprint, err)
	}
	return newVersion, nil
}

// RecordStateChecked advances the throttle window when status hasn't changed.
func (r *IncidentRepo) RecordStateChecked(ctx context.Context, existing model.Incident, checkedAt time.Time) (int64, error) {
	newVersion, err := r.casUpdate(ctx, existing.Fingerprint, existing.Version,
		[]string{"state_checked_at"}, qb.M{
			"state_checked_at": checkedAt,
		})
	if err != nil {
		return existing.Version, fmt.Errorf("record state checked for %s: %w", existing.Fingerprint, err)
	}
	return newVersion, nil
}

// FindByFingerprint reads without mutating, so the engine can decide annotate vs Upsert before touching any row.
func (r *IncidentRepo) FindByFingerprint(ctx context.Context, fp string) (model.Incident, bool, error) {
	return r.get(ctx, fp)
}

// MarkFallbackNotified flips fallback to true once Chat has delivered to every configured target.
func (r *IncidentRepo) MarkFallbackNotified(ctx context.Context, existing model.Incident) (int64, error) {
	newVersion, err := r.casUpdate(ctx, existing.Fingerprint, existing.Version, []string{"fallback"}, qb.M{
		"fallback": true,
	})
	if err != nil {
		return existing.Version, fmt.Errorf("mark incident %s fallback-notified: %w", existing.Fingerprint, err)
	}
	return newVersion, nil
}

// ListPending reads the incidents_pending index instead of scanning the whole incidents_processed table.
func (r *IncidentRepo) ListPending(ctx context.Context) ([]model.Incident, error) {
	stmt, names := qb.Select("incidents_pending").Columns("fingerprint").ToCql()
	var rows []struct {
		Fingerprint string `db:"fingerprint"`
	}
	if err := r.session.Query(stmt, names).WithContext(ctx).SelectRelease(&rows); err != nil {
		return nil, fmt.Errorf("list pending fingerprints: %w", err)
	}
	pending := make([]model.Incident, 0, len(rows))
	for _, row := range rows {
		inc, found, err := r.get(ctx, row.Fingerprint)
		if err != nil {
			return nil, fmt.Errorf("list pending: read incident %s: %w", row.Fingerprint, err)
		}
		if !found {
			continue // index entry outlived its row; skip, nothing to retry.
		}
		if !isPending(inc.CSMConfirmed, inc.CSMPermanentlyFailed, len(inc.PendingNotes)) {
			// Index entry is stale (e.g. a partial failure elsewhere); self-heal so future sweeps don't re-read a delivered incident.
			_ = r.setPendingIndex(ctx, row.Fingerprint, false)
			continue
		}
		pending = append(pending, inc)
	}
	return pending, nil
}

// BackfillVersions sets version=0 on any pre-existing row where it's still NULL (e.g. right after `ALTER TABLE ... ADD version`), since NULL never satisfies casUpdate's "IF version = 0" equality check and would otherwise leave that row permanently stuck returning ErrStaleWrite. Idempotent: an already-backfilled row is skipped.
func (r *IncidentRepo) BackfillVersions(ctx context.Context) error {
	stmt, names := qb.Select("incidents_processed").Columns("fingerprint", "version").ToCql()
	var rows []struct {
		Fingerprint string `db:"fingerprint"`
		Version     *int64 `db:"version"`
	}
	if err := r.session.Query(stmt, names).WithContext(ctx).SelectRelease(&rows); err != nil {
		return fmt.Errorf("backfill versions: list incidents: %w", err)
	}
	for _, row := range rows {
		if row.Version != nil {
			continue
		}
		upd, updNames := qb.Update("incidents_processed").Set("version").Where(qb.Eq("fingerprint")).ToCql()
		if err := r.session.Query(upd, updNames).WithContext(ctx).BindMap(qb.M{
			"fingerprint": row.Fingerprint,
			"version":     int64(0),
		}).ExecRelease(); err != nil {
			return fmt.Errorf("backfill versions: set version for %s: %w", row.Fingerprint, err)
		}
	}
	return nil
}

// BackfillPendingIndex populates incidents_pending for rows that owe delivery before this index existed (one-time cost at startup).
func (r *IncidentRepo) BackfillPendingIndex(ctx context.Context) error {
	stmt, names := qb.Select("incidents_processed").
		Columns("fingerprint", "csm_confirmed", "csm_permanently_failed", "pending_notes").
		ToCql()
	var rows []struct {
		Fingerprint          string   `db:"fingerprint"`
		CSMConfirmed         bool     `db:"csm_confirmed"`
		CSMPermanentlyFailed bool     `db:"csm_permanently_failed"`
		PendingNotes         []string `db:"pending_notes"`
	}
	if err := r.session.Query(stmt, names).WithContext(ctx).SelectRelease(&rows); err != nil {
		return fmt.Errorf("backfill pending index: list incidents: %w", err)
	}
	for _, row := range rows {
		if !isPending(row.CSMConfirmed, row.CSMPermanentlyFailed, len(row.PendingNotes)) {
			continue
		}
		if err := r.setPendingIndex(ctx, row.Fingerprint, true); err != nil {
			return fmt.Errorf("backfill pending index: mark %s pending: %w", row.Fingerprint, err)
		}
	}
	return nil
}

// AppendWorkNote appends to both work_notes (audit log) and pending_notes (delivery queue) since Cassandra lacks native list append.
func (r *IncidentRepo) AppendWorkNote(ctx context.Context, existing model.Incident, note string) (int64, error) {
	updatedNotes := capTail(append(append([]string{}, existing.WorkNotes...), note), maxWorkNotes)
	updatedPending := capTail(append(append([]string{}, existing.PendingNotes...), note), maxWorkNotes)
	newVersion, err := r.casUpdate(ctx, existing.Fingerprint, existing.Version,
		[]string{"work_notes", "pending_notes"}, qb.M{
			"work_notes":    updatedNotes,
			"pending_notes": updatedPending,
		})
	if err != nil {
		return existing.Version, fmt.Errorf("append work note to incident %s: %w", existing.Fingerprint, err)
	}
	// Best-effort: note is durable, so a missed index sync doesn't affect delivery state.
	_ = r.setPendingIndex(ctx, existing.Fingerprint, isPending(existing.CSMConfirmed, existing.CSMPermanentlyFailed, len(updatedPending)))
	return newVersion, nil
}

// ClearPendingNotes persists unpushed notes after a push attempt (empty on full success, unpushed suffix on partial failure).
func (r *IncidentRepo) ClearPendingNotes(ctx context.Context, existing model.Incident, remaining []string) (int64, error) {
	newVersion, err := r.casUpdate(ctx, existing.Fingerprint, existing.Version, []string{"pending_notes"}, qb.M{
		"pending_notes": remaining,
	})
	if err != nil {
		return existing.Version, fmt.Errorf("clear pending notes for %s: %w", existing.Fingerprint, err)
	}
	// Best-effort, same reasoning as the other index-sync calls above.
	_ = r.syncPendingIndex(ctx, existing.Fingerprint)
	return newVersion, nil
}

// get reads the incident for a fingerprint, reporting absence as false rather than an error.
func (r *IncidentRepo) get(ctx context.Context, fp string) (model.Incident, bool, error) {
	stmt, names := qb.Select("incidents_processed").Columns(incidentColumns...).Where(qb.Eq("fingerprint")).ToCql()
	var inc model.Incident
	err := r.session.Query(stmt, names).WithContext(ctx).BindMap(qb.M{"fingerprint": fp}).GetRelease(&inc)
	if err == gocql.ErrNotFound {
		return model.Incident{}, false, nil
	}
	if err != nil {
		return model.Incident{}, false, fmt.Errorf("read incident %s: %w", fp, err)
	}
	return inc, true, nil
}
