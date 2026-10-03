// Copyright (c) 2026 WSO2 LLC. (https://www.wso2.com).
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

package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// IncidentOutboxEntityType is the event_outbox entity_type migration 0181's
// trigger writes for incident row changes (trg_event_outbox uses
// TG_TABLE_NAME).
const IncidentOutboxEntityType = "incident"

// ErrIncidentNotFound is returned by IncidentReportTx.IncidentSource when the
// incident an outbox row names no longer exists.
var ErrIncidentNotFound = errors.New("incident not found")

// Retry backoff for a failed incident change, as applied by PendingChanges:
// 30s after the first failure, doubling each time, capped at 1h. With
// service.IncidentReportMaxAttempts = 10 a row keeps being retried for about
// three hours before it is parked.

// IncidentReportChange is one incident row change read from event_outbox.
type IncidentReportChange struct {
	OutboxID   int64
	IncidentID string
	// Changes is {"column": {"from": …, "to": …}} for the columns that differ.
	Changes    map[string]map[string]any
	OccurredOn time.Time
	// Attempts is how many earlier passes failed on this row.
	Attempts int
}

// IncidentReportSource is what the two incident report flows read off the
// triggering incident. number, assignment group and assignee live on
// work_item; priority and service on incident.
type IncidentReportSource struct {
	IncidentID        string
	Number            string
	Priority          *string
	CreatedOn         time.Time
	ServiceID         *string
	AssignmentGroupID *string
	AssignedToID      *string
}

// NewIncidentReportTask is the incident_task the "Create Incident Report
// Task" flow creates.
type NewIncidentReportTask struct {
	IncidentID        string
	Subject           string
	ServiceID         *string
	AssignmentGroupID *string
	AssignedToID      *string
	CreatedBy         string
}

// IncidentReportTx is the work one outbox row may do. Every call runs in the
// transaction that also marks the row processed, so the effect and the mark
// commit or roll back together.
type IncidentReportTx interface {
	IncidentSource(ctx context.Context, incidentID string) (IncidentReportSource, error)
	// CreateReportTask inserts the work_item + incident_task pair and returns
	// the new task's id and number.
	CreateReportTask(ctx context.Context, task NewIncidentReportTask) (id, number string, err error)
	SetIncidentReport(ctx context.Context, incidentID, report, updatedBy string) error
}

// IncidentReportRepository reads incident changes from event_outbox and
// applies the incident report flows' writes.
//
// Unlike ClaimChanges (cr_notice_repo.go), a row is marked published only in
// the transaction that applied its effect, never at claim time: a crash or a
// failed write leaves the row unpublished and the next pass retries it. See
// migration 0181 for why the incident flows need that and the notices do not.
type IncidentReportRepository interface {
	// PendingChanges lists unpublished incident outbox ids that are due,
	// oldest first: a row that failed before waits out its backoff
	// (IncidentReportRetryDelay). It takes no locks; ProcessChange does the
	// claiming.
	PendingChanges(ctx context.Context, limit int) ([]int64, error)
	// ProcessChange locks one outbox row and, if it is still unpublished,
	// runs fn and marks the row published in one transaction. It returns
	// false without calling fn when another replica holds or has already
	// processed the row.
	ProcessChange(ctx context.Context, outboxID int64, fn func(ctx context.Context, tx IncidentReportTx, c IncidentReportChange) error) (bool, error)
	// RecordFailure counts a failed attempt and stores the error. Once
	// attempts reach maxAttempts the row is marked published so it stops
	// being retried; last_error keeps the reason. Returns whether it parked.
	RecordFailure(ctx context.Context, outboxID int64, cause string, maxAttempts int) (bool, error)
	// MissingSchema lists the parts of migration 0181 that are not in the
	// database: the three event_outbox retry columns and the incident_outbox
	// trigger. Empty means the drainer can run.
	MissingSchema(ctx context.Context) ([]string, error)
}

type incidentReportRepository struct {
	db *Scoped
}

// NewIncidentReportRepository constructs the incident report repository.
func NewIncidentReportRepository(db *Scoped) IncidentReportRepository {
	return &incidentReportRepository{db: db}
}

// PendingChanges implements IncidentReportRepository.
func (r *incidentReportRepository) PendingChanges(ctx context.Context, limit int) ([]int64, error) {
	// WithSystemIdentity: a background drainer, with no viewer to speak for
	// -- same reasoning as crNoticeRepository.ClaimChanges.
	ctx = WithSystemIdentity(ctx)
	rows, err := r.db.Query(ctx, `
		SELECT id FROM event_outbox
		WHERE published_on IS NULL AND entity_type = $1
		  AND (last_attempt_on IS NULL
		       OR last_attempt_on + LEAST(INTERVAL '1 hour',
		                                  INTERVAL '30 seconds' * POWER(2, GREATEST(attempts - 1, 0)))
		          <= NOW())
		ORDER BY id
		LIMIT $2`, IncidentOutboxEntityType, limit)
	if err != nil {
		return nil, fmt.Errorf("incidentreport: list pending changes: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("incidentreport: scan pending change: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ProcessChange implements IncidentReportRepository.
func (r *incidentReportRepository) ProcessChange(ctx context.Context, outboxID int64, fn func(ctx context.Context, tx IncidentReportTx, c IncidentReportChange) error) (bool, error) {
	ctx = WithSystemIdentity(ctx)
	processed := false
	err := r.db.InTx(ctx, func(tx pgx.Tx) error {
		var (
			c          IncidentReportChange
			changesRaw []byte
		)
		// SKIP LOCKED: a row another replica is processing right now is not
		// waited on -- it is that replica's. published_on IS NULL re-checks
		// under the lock, so a row finished between PendingChanges and here
		// is skipped rather than applied twice.
		err := tx.QueryRow(ctx, `
			SELECT id, entity_id, changes, occurred_on, attempts
			FROM event_outbox
			WHERE id = $1 AND published_on IS NULL
			FOR UPDATE SKIP LOCKED`, outboxID).
			Scan(&c.OutboxID, &c.IncidentID, &changesRaw, &c.OccurredOn, &c.Attempts)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("incidentreport: lock outbox %d: %w", outboxID, err)
		}
		if err := json.Unmarshal(changesRaw, &c.Changes); err != nil {
			return fmt.Errorf("incidentreport: decode changes for outbox %d: %w", outboxID, err)
		}
		if err := fn(ctx, incidentReportTx{tx: tx}, c); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE event_outbox SET published_on = NOW() WHERE id = $1`, outboxID); err != nil {
			return fmt.Errorf("incidentreport: mark outbox %d published: %w", outboxID, err)
		}
		processed = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return processed, nil
}

// RecordFailure implements IncidentReportRepository.
func (r *incidentReportRepository) RecordFailure(ctx context.Context, outboxID int64, cause string, maxAttempts int) (bool, error) {
	ctx = WithSystemIdentity(ctx)
	var parked bool
	err := r.db.QueryRow(ctx, `
		UPDATE event_outbox
		SET attempts = attempts + 1,
		    last_error = $2,
		    last_attempt_on = NOW(),
		    published_on = CASE WHEN attempts + 1 >= $3 THEN NOW() ELSE published_on END
		WHERE id = $1 AND published_on IS NULL
		RETURNING published_on IS NOT NULL`, outboxID, cause, maxAttempts).Scan(&parked)
	if errors.Is(err, pgx.ErrNoRows) {
		// Another replica finished the row in the meantime: nothing to count.
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("incidentreport: record failure for outbox %d: %w", outboxID, err)
	}
	return parked, nil
}

type incidentReportTx struct {
	tx pgx.Tx
}

// IncidentSource implements IncidentReportTx.
func (t incidentReportTx) IncidentSource(ctx context.Context, incidentID string) (IncidentReportSource, error) {
	s := IncidentReportSource{IncidentID: incidentID}
	err := t.tx.QueryRow(ctx, `
		SELECT wi.number, i.priority::text, wi.created_on,
		       i.service_id::text, wi.assignment_group_id::text, wi.assigned_to_id::text
		FROM incident i
		JOIN work_item wi ON wi.id = i.id
		WHERE i.id = $1`, incidentID).
		Scan(&s.Number, &s.Priority, &s.CreatedOn, &s.ServiceID, &s.AssignmentGroupID, &s.AssignedToID)
	if errors.Is(err, pgx.ErrNoRows) {
		return IncidentReportSource{}, ErrIncidentNotFound
	}
	if err != nil {
		return IncidentReportSource{}, fmt.Errorf("incidentreport: read incident %s: %w", incidentID, err)
	}
	return s, nil
}

// CreateReportTask implements IncidentReportTx.
//
// The number comes from next_portal_work_item_number() (migration 0140), the
// series every record created in this service rather than synced uses.
// wso2_id stays NULL: work_item_wso2_id_required_by_type does not cover
// INCIDENT_TASK. state OPEN / active mirror ServiceNow's defaults for a new
// incident_task, since the flow's Create Record step leaves both unset.
func (t incidentReportTx) CreateReportTask(ctx context.Context, task NewIncidentReportTask) (string, string, error) {
	var id, number string
	err := t.tx.QueryRow(ctx, `
		WITH inserted_work_item AS (
			INSERT INTO work_item (
				id, created_on, updated_on, created_by, updated_by,
				number, subject, type, assignment_group_id, assigned_to_id
			)
			VALUES (
				gen_random_uuid(), NOW(), NOW(), $1, $1,
				next_portal_work_item_number(), $2, 'INCIDENT_TASK'::work_item_type_enum,
				$3::uuid, $4::uuid
			)
			RETURNING id, number
		),
		inserted_task AS (
			INSERT INTO incident_task (
				id, opened_on, priority, state, is_active, incident_id, service_id, type
			)
			SELECT id, NOW(), 'CRITICAL', 'OPEN', TRUE, $5::uuid, $6::uuid, 'INCIDENT_REPORT'
			FROM inserted_work_item
			RETURNING id
		)
		SELECT iwi.id::text, iwi.number
		FROM inserted_work_item iwi
		JOIN inserted_task it ON it.id = iwi.id`,
		task.CreatedBy, task.Subject, task.AssignmentGroupID, task.AssignedToID,
		task.IncidentID, task.ServiceID).Scan(&id, &number)
	if err != nil {
		return "", "", fmt.Errorf("incidentreport: create report task for incident %s: %w", task.IncidentID, err)
	}
	return id, number, nil
}

// SetIncidentReport implements IncidentReportTx.
//
// Touches work_item.updated_on/updated_by too, as any other edit of the
// incident would. incident itself has no audit columns (they live on
// work_item), so this UPDATE on incident fires the 0181 trigger with only
// incident_report in its diff -- which carries no state change and is
// ignored by the drainer, not looped on.
func (t incidentReportTx) SetIncidentReport(ctx context.Context, incidentID, report, updatedBy string) error {
	tag, err := t.tx.Exec(ctx, `UPDATE incident SET incident_report = $2 WHERE id = $1`, incidentID, report)
	if err != nil {
		return fmt.Errorf("incidentreport: write report for incident %s: %w", incidentID, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrIncidentNotFound
	}
	if _, err := t.tx.Exec(ctx, `UPDATE work_item SET updated_on = NOW(), updated_by = $2 WHERE id = $1`, incidentID, updatedBy); err != nil {
		return fmt.Errorf("incidentreport: touch work_item for incident %s: %w", incidentID, err)
	}
	return nil
}

// MissingSchema implements IncidentReportRepository.
//
// Checked at drainer start and after any schema fault, so deploying this code
// ahead of migration 0181 idles the drainer with one clear log line instead
// of failing every poll (which is what staging saw on 2026-10-03).
func (r *incidentReportRepository) MissingSchema(ctx context.Context) ([]string, error) {
	ctx = WithSystemIdentity(ctx)
	rows, err := r.db.Query(ctx, `
		SELECT need FROM (VALUES
			('event_outbox.attempts'),
			('event_outbox.last_error'),
			('event_outbox.last_attempt_on'),
			('incident trigger incident_outbox')
		) AS v(need)
		WHERE NOT (
			CASE WHEN need LIKE 'event_outbox.%' THEN EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = current_schema() AND table_name = 'event_outbox'
				  AND column_name = split_part(need, '.', 2))
			ELSE EXISTS (
				SELECT 1 FROM pg_trigger
				WHERE tgname = 'incident_outbox' AND tgrelid = to_regclass('incident') AND NOT tgisinternal)
			END)
		ORDER BY need`)
	if err != nil {
		return nil, fmt.Errorf("incidentreport: check schema: %w", err)
	}
	defer rows.Close()
	var missing []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, fmt.Errorf("incidentreport: scan schema check: %w", err)
		}
		missing = append(missing, m)
	}
	return missing, rows.Err()
}
