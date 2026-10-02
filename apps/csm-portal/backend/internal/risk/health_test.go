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

package risk

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func newTestClient(t *testing.T) (*Client, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &Client{db: db}, mock
}

func riskRowCols() []string {
	return []string{"id", "project_sys_id", "account_sys_id", "status", "opened_comment", "opened_by_email",
		"opened_on", "closed_comment", "closed_by_email", "closed_on"}
}

func TestOpenProjectRisk_CommitsTransaction(t *testing.T) {
	c, mock := newTestClient(t)
	ctx := context.Background()
	now := time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id FROM project_risk WHERE project_sys_id = \\? AND account_sys_id = \\? AND status = 'open' FOR UPDATE").
		WithArgs("proj-1", "acct-1").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO project_risk").
		WithArgs("proj-1", "acct-1", "at risk", "user@example.com").
		WillReturnResult(sqlmock.NewResult(42, 1))
	mock.ExpectExec("INSERT INTO project_health_status").
		WithArgs("proj-1", "acct-1", "user@example.com", "user@example.com").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	mock.ExpectQuery("SELECT (.+) FROM project_risk WHERE id = ?").
		WithArgs(42).
		WillReturnRows(sqlmock.NewRows(riskRowCols()).
			AddRow(42, "proj-1", "acct-1", "open", "at risk", "user@example.com", now, nil, nil, nil))
	mock.ExpectQuery("SELECT (.+) FROM risk_action_item WHERE risk_id = ?").
		WithArgs(42).
		WillReturnRows(sqlmock.NewRows([]string{"id", "risk_id", "project_sys_id", "account_sys_id", "title",
			"description", "priority", "status", "assigned_to_email", "due_date", "resolution_comment",
			"resolved_by_email", "resolved_on", "created_by_email", "created_on", "updated_on"}))

	risk, err := c.OpenProjectRisk(ctx, "proj-1", "acct-1", "at risk", "user@example.com")
	if err != nil {
		t.Fatalf("OpenProjectRisk returned error: %v", err)
	}
	if risk.ID != 42 || risk.Status != "open" {
		t.Errorf("risk = %+v, want ID=42 Status=open", risk)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestOpenProjectRisk_RollsBackOnSecondInsertFailure(t *testing.T) {
	c, mock := newTestClient(t)
	ctx := context.Background()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id FROM project_risk WHERE project_sys_id = \\? AND account_sys_id = \\? AND status = 'open' FOR UPDATE").
		WithArgs("proj-1", "acct-1").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("INSERT INTO project_risk").
		WithArgs("proj-1", "acct-1", "at risk", "user@example.com").
		WillReturnResult(sqlmock.NewResult(42, 1))
	mock.ExpectExec("INSERT INTO project_health_status").
		WillReturnError(errors.New("connection lost"))
	mock.ExpectRollback()

	_, err := c.OpenProjectRisk(ctx, "proj-1", "acct-1", "at risk", "user@example.com")
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations (rollback not observed?): %v", err)
	}
}

func TestOpenProjectRisk_RejectsWhenAlreadyOpen(t *testing.T) {
	c, mock := newTestClient(t)
	ctx := context.Background()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id FROM project_risk WHERE project_sys_id = \\? AND account_sys_id = \\? AND status = 'open' FOR UPDATE").
		WithArgs("proj-1", "acct-1").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(7))
	mock.ExpectRollback()

	_, err := c.OpenProjectRisk(ctx, "proj-1", "acct-1", "at risk", "user@example.com")
	var valErr *ValidationError
	if !errors.As(err, &valErr) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	if valErr.Message != "Project already has an open risk: 7" {
		t.Errorf("message = %q", valErr.Message)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations (rollback not observed?): %v", err)
	}
}

func TestCloseProjectRisk_RejectsWhenActionItemsStillOpen(t *testing.T) {
	c, mock := newTestClient(t)
	ctx := context.Background()
	now := time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT (.+) FROM project_risk WHERE id = ?").
		WithArgs(7).
		WillReturnRows(sqlmock.NewRows(riskRowCols()).
			AddRow(7, "proj-1", "acct-1", "open", "at risk", "user@example.com", now, nil, nil, nil))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM risk_action_item").
		WithArgs(7).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectRollback()

	_, err := c.CloseProjectRisk(ctx, 7, "resolved", "user@example.com")
	var valErr *ValidationError
	if !errors.As(err, &valErr) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	if valErr.Message != "Cannot close risk: 2 action item(s) are still open." {
		t.Errorf("message = %q", valErr.Message)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations (rollback not observed?): %v", err)
	}
}

func TestCloseProjectRisk_MissingRiskIsValidationError(t *testing.T) {
	c, mock := newTestClient(t)
	ctx := context.Background()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT (.+) FROM project_risk WHERE id = ?").
		WithArgs(999).
		WillReturnRows(sqlmock.NewRows(riskRowCols()))
	mock.ExpectRollback()

	_, err := c.CloseProjectRisk(ctx, 999, "resolved", "user@example.com")
	var valErr *ValidationError
	if !errors.As(err, &valErr) {
		t.Fatalf("expected *ValidationError (not a generic/not-found error), got %T: %v", err, err)
	}
	if valErr.Message != "Risk not found: 999" {
		t.Errorf("message = %q", valErr.Message)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations (rollback not observed?): %v", err)
	}
}

func TestCloseProjectRisk_RejectsWhenRiskNotOpen(t *testing.T) {
	c, mock := newTestClient(t)
	ctx := context.Background()
	now := time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT (.+) FROM project_risk WHERE id = ?").
		WithArgs(7).
		WillReturnRows(sqlmock.NewRows(riskRowCols()).
			AddRow(7, "proj-1", "acct-1", "closed", "at risk", "user@example.com", now, "fixed", "user@example.com", now))
	mock.ExpectRollback()

	_, err := c.CloseProjectRisk(ctx, 7, "resolved", "user@example.com")
	var valErr *ValidationError
	if !errors.As(err, &valErr) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	if valErr.Message != "Risk 7 is not open." {
		t.Errorf("message = %q", valErr.Message)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations (rollback not observed?): %v", err)
	}
}

func TestGetAccountHealthSummary_DerivesOverallStatus(t *testing.T) {
	tests := []struct {
		name     string
		statuses []string
		want     string
	}{
		{"no rows", nil, "to_be_reviewed"},
		{"any at_risk wins", []string{"healthy", "at_risk"}, "at_risk"},
		{"all healthy", []string{"healthy", "healthy"}, "healthy"},
		{"mixed non-at-risk", []string{"healthy", "to_be_reviewed"}, "to_be_reviewed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, mock := newTestClient(t)
			rows := sqlmock.NewRows([]string{"status"})
			for _, s := range tt.statuses {
				rows.AddRow(s)
			}
			mock.ExpectQuery("SELECT status FROM project_health_status").WithArgs("acct-1").WillReturnRows(rows)

			summary, err := c.GetAccountHealthSummary(context.Background(), "acct-1")
			if err != nil {
				t.Fatalf("GetAccountHealthSummary returned error: %v", err)
			}
			if summary.OverallStatus != tt.want {
				t.Errorf("OverallStatus = %q, want %q", summary.OverallStatus, tt.want)
			}
		})
	}
}

func TestGetBatchHealthSummaries_EmptyInputSkipsQuery(t *testing.T) {
	c, mock := newTestClient(t)
	result, err := c.GetBatchHealthSummaries(context.Background(), nil)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected empty map, got %v", result)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected DB call: %v", err)
	}
}

func TestGetAccountsByHealthStatus_UnknownStatusReturnsEmpty(t *testing.T) {
	c, mock := newTestClient(t)
	ids, err := c.GetAccountsByHealthStatus(context.Background(), "bogus")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("expected empty slice, got %v", ids)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unexpected DB call: %v", err)
	}
}
