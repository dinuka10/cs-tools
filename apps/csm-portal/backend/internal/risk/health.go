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
	"fmt"
	"strings"
)

const projectRiskColumns = "id, project_sys_id, account_sys_id, status, opened_comment, opened_by_email, opened_on, closed_comment, closed_by_email, closed_on"

const healthStatusColumns = "id, project_sys_id, account_sys_id, status, reviewed_by_email, reviewed_on"

func scanProjectRiskRow(row interface{ Scan(...any) error }) (projectRiskRow, error) {
	var r projectRiskRow
	err := row.Scan(&r.ID, &r.ProjectSysID, &r.AccountSysID, &r.Status, &r.OpenedComment, &r.OpenedByEmail,
		&r.OpenedOn, &r.ClosedComment, &r.ClosedByEmail, &r.ClosedOn)
	return r, err
}

func scanHealthStatusRow(row interface{ Scan(...any) error }) (healthStatusRow, error) {
	var r healthStatusRow
	err := row.Scan(&r.ID, &r.ProjectSysID, &r.AccountSysID, &r.Status, &r.ReviewedByEmail, &r.ReviewedOn)
	return r, err
}

// OpenProjectRisk opens a new risk record for a project and sets the
// project's health status to "at_risk", in one transaction. Fails with
// *ValidationError if the project already has an open risk -- the existing
// open row (if any) is locked with SELECT ... FOR UPDATE before the insert,
// so two concurrent calls (or a double click) can't both open a risk for the
// same project: the second call's lookup blocks until the first's
// transaction commits, then sees the row it just inserted.
func (c *Client) OpenProjectRisk(ctx context.Context, projectSysID, accountSysID, comment, email string) (*ProjectRisk, error) {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("risk: begin transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	var existingID int
	err = tx.QueryRowContext(ctx,
		"SELECT id FROM project_risk WHERE project_sys_id = ? AND account_sys_id = ? AND status = 'open' FOR UPDATE",
		projectSysID, accountSysID).Scan(&existingID)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("risk: check existing open risk: %w", err)
	}
	if err == nil {
		return nil, &ValidationError{Message: fmt.Sprintf("Project already has an open risk: %d", existingID)}
	}

	execResult, err := tx.ExecContext(ctx, `
		INSERT INTO project_risk (project_sys_id, account_sys_id, status, opened_comment, opened_by_email, opened_on)
		VALUES (?, ?, 'open', ?, ?, NOW())`, projectSysID, accountSysID, comment, email)
	if err != nil {
		return nil, fmt.Errorf("risk: insert project_risk: %w", err)
	}
	riskID, err := execResult.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("risk: read inserted risk id: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO project_health_status (project_sys_id, account_sys_id, status, reviewed_by_email, reviewed_on)
		VALUES (?, ?, 'at_risk', ?, NOW())
		ON DUPLICATE KEY UPDATE status = 'at_risk', reviewed_by_email = ?, reviewed_on = NOW()`,
		projectSysID, accountSysID, email, email)
	if err != nil {
		return nil, fmt.Errorf("risk: upsert project_health_status: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("risk: commit transaction: %w", err)
	}
	committed = true

	return c.getRiskByID(ctx, int(riskID))
}

// CloseProjectRisk closes an open risk for a project. Fails with
// *ValidationError if the risk doesn't exist, is not currently open, or if
// any of its action items are still open/in_progress — mirroring the
// Ballerina source's use of SupportLiteBadRequest (not NotFound) for a
// missing risk here. The risk row is locked (SELECT ... FOR UPDATE) and the
// open-action-item count is taken inside the same transaction as the
// close, so an action item created concurrently with this call can't leave
// the risk closed with a still-open item, and closing an already-closed
// risk can't overwrite its closed_* audit fields or wrongly reset the
// project's health status.
func (c *Client) CloseProjectRisk(ctx context.Context, riskID int, comment, email string) (*ProjectRisk, error) {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("risk: begin transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	row := tx.QueryRowContext(ctx, "SELECT "+projectRiskColumns+" FROM project_risk WHERE id = ? FOR UPDATE", riskID)
	riskRow, err := scanProjectRiskRow(row)
	if err == sql.ErrNoRows {
		return nil, &ValidationError{Message: fmt.Sprintf("Risk not found: %d", riskID)}
	}
	if err != nil {
		return nil, fmt.Errorf("risk: query project_risk by id: %w", err)
	}
	if riskRow.Status != "open" {
		return nil, &ValidationError{Message: fmt.Sprintf("Risk %d is not open.", riskID)}
	}

	var openCount int
	err = tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM risk_action_item WHERE risk_id = ? AND status NOT IN ('resolved', 'cancelled')`,
		riskID).Scan(&openCount)
	if err != nil {
		return nil, fmt.Errorf("risk: count open action items: %w", err)
	}
	if openCount > 0 {
		return nil, &ValidationError{Message: fmt.Sprintf("Cannot close risk: %d action item(s) are still open.", openCount)}
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE project_risk
		SET status = 'closed', closed_comment = ?, closed_by_email = ?, closed_on = NOW()
		WHERE id = ? AND status = 'open'`, comment, email, riskID)
	if err != nil {
		return nil, fmt.Errorf("risk: close project_risk: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE project_health_status
		SET status = 'to_be_reviewed', reviewed_by_email = NULL, reviewed_on = NULL
		WHERE project_sys_id = ? AND account_sys_id = ?`, riskRow.ProjectSysID, riskRow.AccountSysID)
	if err != nil {
		return nil, fmt.Errorf("risk: reset project_health_status: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("risk: commit transaction: %w", err)
	}
	committed = true

	return c.getRiskByID(ctx, riskID)
}

// MarkProjectHealthy marks a project healthy and records a closed risk
// entry for audit history, in one transaction.
func (c *Client) MarkProjectHealthy(ctx context.Context, projectSysID, accountSysID, email string, comment *string) (*HealthStatusRecord, error) {
	closedComment := "Reviewed and confirmed healthy"
	if comment != nil {
		closedComment = *comment
	}

	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("risk: begin transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	_, err = tx.ExecContext(ctx, `
		INSERT INTO project_health_status (project_sys_id, account_sys_id, status, reviewed_by_email, reviewed_on)
		VALUES (?, ?, 'healthy', ?, NOW())
		ON DUPLICATE KEY UPDATE status = 'healthy', reviewed_by_email = ?, reviewed_on = NOW()`,
		projectSysID, accountSysID, email, email)
	if err != nil {
		return nil, fmt.Errorf("risk: upsert project_health_status: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO project_risk
			(project_sys_id, account_sys_id, status, opened_comment, opened_by_email, opened_on,
			 closed_comment, closed_by_email, closed_on)
		VALUES (?, ?, 'closed', 'Marked as healthy', ?, NOW(), ?, ?, NOW())`,
		projectSysID, accountSysID, email, closedComment, email)
	if err != nil {
		return nil, fmt.Errorf("risk: insert closed project_risk: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("risk: commit transaction: %w", err)
	}
	committed = true

	return c.getHealthStatusByProject(ctx, projectSysID, accountSysID)
}

// RevertProjectHealth reverts a project's health status back to
// "to_be_reviewed".
func (c *Client) RevertProjectHealth(ctx context.Context, projectSysID, accountSysID string) (*HealthStatusRecord, error) {
	result, err := c.db.ExecContext(ctx, `
		UPDATE project_health_status
		SET status = 'to_be_reviewed', reviewed_by_email = NULL, reviewed_on = NULL
		WHERE project_sys_id = ? AND account_sys_id = ?`, projectSysID, accountSysID)
	if err != nil {
		return nil, fmt.Errorf("risk: reset project_health_status: %w", err)
	}
	// A mismatched (projectSysID, accountSysID) pair updates zero rows --
	// without this check the caller would otherwise get back whatever
	// getHealthStatusByProject's own account scoping below still finds (or a
	// misleading errRecordNotFound), instead of a clear signal that nothing
	// was reverted.
	if affected, err := result.RowsAffected(); err != nil {
		return nil, fmt.Errorf("risk: reset project_health_status: rows affected: %w", err)
	} else if affected == 0 {
		return nil, errRecordNotFound
	}
	return c.getHealthStatusByProject(ctx, projectSysID, accountSysID)
}

// GetAccountHealthStatus retrieves the health status for every project
// under an account, including each project's currently open risk, if any.
func (c *Client) GetAccountHealthStatus(ctx context.Context, accountSysID string) ([]ProjectHealthStatus, error) {
	rows, err := c.db.QueryContext(ctx,
		"SELECT "+healthStatusColumns+" FROM project_health_status WHERE account_sys_id = ?", accountSysID)
	if err != nil {
		return nil, fmt.Errorf("risk: query project_health_status: %w", err)
	}
	defer rows.Close()

	var statusRows []healthStatusRow
	for rows.Next() {
		r, err := scanHealthStatusRow(rows)
		if err != nil {
			return nil, fmt.Errorf("risk: scan project_health_status row: %w", err)
		}
		statusRows = append(statusRows, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("risk: iterate project_health_status rows: %w", err)
	}

	results := make([]ProjectHealthStatus, 0, len(statusRows))
	for _, statusRow := range statusRows {
		healthStatus := mapHealthStatusRowToHealthStatus(statusRow)
		var openRisk *ProjectRisk

		if statusRow.Status == "at_risk" {
			riskRow, err := c.queryOpenRiskForProject(ctx, statusRow.ProjectSysID, statusRow.AccountSysID)
			if err != nil {
				return nil, err
			}
			if riskRow != nil {
				actionItems, err := c.getActionItemsByRiskID(ctx, riskRow.ID)
				if err != nil {
					return nil, err
				}
				risk := mapRiskRowToRisk(*riskRow, actionItems)
				openRisk = &risk
			}
		}

		results = append(results, ProjectHealthStatus{
			ProjectSysID: statusRow.ProjectSysID,
			HealthStatus: healthStatus,
			OpenRisk:     openRisk,
		})
	}
	return results, nil
}

func (c *Client) queryOpenRiskForProject(ctx context.Context, projectSysID, accountSysID string) (*projectRiskRow, error) {
	row := c.db.QueryRowContext(ctx,
		"SELECT "+projectRiskColumns+" FROM project_risk WHERE project_sys_id = ? AND account_sys_id = ? AND status = 'open' ORDER BY opened_on DESC LIMIT 1",
		projectSysID, accountSysID)
	r, err := scanProjectRiskRow(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("risk: query open project_risk: %w", err)
	}
	return &r, nil
}

// GetAccountHealthSummary retrieves an account's aggregated overall health
// status derived from all its projects' individual statuses.
func (c *Client) GetAccountHealthSummary(ctx context.Context, accountSysID string) (*HealthSummary, error) {
	rows, err := c.db.QueryContext(ctx,
		"SELECT status FROM project_health_status WHERE account_sys_id = ?", accountSysID)
	if err != nil {
		return nil, fmt.Errorf("risk: query project_health_status: %w", err)
	}
	defer rows.Close()

	var statuses []string
	for rows.Next() {
		var status string
		if err := rows.Scan(&status); err != nil {
			return nil, fmt.Errorf("risk: scan status: %w", err)
		}
		statuses = append(statuses, status)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("risk: iterate project_health_status rows: %w", err)
	}

	if len(statuses) == 0 {
		return &HealthSummary{AccountSysID: accountSysID, OverallStatus: "to_be_reviewed"}, nil
	}

	hasAtRisk := false
	allHealthy := true
	for _, status := range statuses {
		if status == "at_risk" {
			hasAtRisk = true
		}
		if status != "healthy" {
			allHealthy = false
		}
	}

	overallStatus := "to_be_reviewed"
	switch {
	case hasAtRisk:
		overallStatus = "at_risk"
	case allHealthy:
		overallStatus = "healthy"
	}
	return &HealthSummary{AccountSysID: accountSysID, OverallStatus: overallStatus}, nil
}

// GetProjectRiskHistory retrieves the full risk history for a project,
// newest first.
func (c *Client) GetProjectRiskHistory(ctx context.Context, projectSysID string) ([]ProjectRisk, error) {
	rows, err := c.db.QueryContext(ctx,
		"SELECT "+projectRiskColumns+" FROM project_risk WHERE project_sys_id = ? ORDER BY opened_on DESC", projectSysID)
	if err != nil {
		return nil, fmt.Errorf("risk: query project_risk: %w", err)
	}
	defer rows.Close()

	var riskRows []projectRiskRow
	for rows.Next() {
		r, err := scanProjectRiskRow(rows)
		if err != nil {
			return nil, fmt.Errorf("risk: scan project_risk row: %w", err)
		}
		riskRows = append(riskRows, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("risk: iterate project_risk rows: %w", err)
	}

	risks := make([]ProjectRisk, 0, len(riskRows))
	for _, riskRow := range riskRows {
		actionItems, err := c.getActionItemsByRiskID(ctx, riskRow.ID)
		if err != nil {
			return nil, err
		}
		risks = append(risks, mapRiskRowToRisk(riskRow, actionItems))
	}
	return risks, nil
}

// GetBatchHealthSummaries retrieves the overall health status for a batch
// of accounts in a single query, returning a map keyed by account sys_id.
func (c *Client) GetBatchHealthSummaries(ctx context.Context, accountSysIDs []string) (map[string]string, error) {
	result := make(map[string]string, len(accountSysIDs))
	if len(accountSysIDs) == 0 {
		return result, nil
	}

	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(accountSysIDs)), ",")
	args := make([]any, len(accountSysIDs))
	for i, id := range accountSysIDs {
		args[i] = id
	}

	// #nosec G202 -- placeholders is built only from literal "?" and ","
	// characters (strings.Repeat above), never from accountSysIDs; every
	// actual value is passed as a parameterized arg via args... below. This
	// is the standard database/sql pattern for a variadic IN (...) clause,
	// whose placeholder count is only known at runtime.
	rows, err := c.db.QueryContext(ctx,
		"SELECT account_sys_id, status FROM project_health_status WHERE account_sys_id IN ("+placeholders+")", args...)
	if err != nil {
		return nil, fmt.Errorf("risk: query batch health summaries: %w", err)
	}
	defer rows.Close()

	hasAtRisk := make(map[string]bool)
	hasNonHealthy := make(map[string]bool)
	hasAnyRow := make(map[string]bool)
	for rows.Next() {
		var accountSysID, status string
		if err := rows.Scan(&accountSysID, &status); err != nil {
			return nil, fmt.Errorf("risk: scan batch health summary row: %w", err)
		}
		hasAnyRow[accountSysID] = true
		if status == "at_risk" {
			hasAtRisk[accountSysID] = true
		}
		if status != "healthy" {
			hasNonHealthy[accountSysID] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("risk: iterate batch health summary rows: %w", err)
	}

	for _, accountSysID := range accountSysIDs {
		switch {
		case hasAtRisk[accountSysID]:
			result[accountSysID] = "at_risk"
		case hasAnyRow[accountSysID] && !hasNonHealthy[accountSysID]:
			result[accountSysID] = "healthy"
		default:
			result[accountSysID] = "to_be_reviewed"
		}
	}
	return result, nil
}

// GetAccountsByHealthStatus retrieves the account sys_ids matching a given
// health status ("at_risk" or "healthy"); any other value returns an empty
// list, mirroring the Ballerina source.
func (c *Client) GetAccountsByHealthStatus(ctx context.Context, healthStatus string) ([]string, error) {
	var query string
	switch healthStatus {
	case "at_risk":
		query = "SELECT DISTINCT account_sys_id FROM project_risk WHERE status = 'open'"
	case "healthy":
		query = `SELECT DISTINCT account_sys_id FROM project_health_status
			WHERE account_sys_id NOT IN (
				SELECT DISTINCT account_sys_id FROM project_health_status WHERE status != 'healthy'
			)`
	default:
		return []string{}, nil
	}

	rows, err := c.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("risk: query accounts by health status: %w", err)
	}
	defer rows.Close()

	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("risk: scan account sys_id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("risk: iterate account sys_id rows: %w", err)
	}
	return ids, nil
}

// InitProjectHealthRows inserts a "to_be_reviewed" health-status row for
// every project sys_id that doesn't already have one under the given
// account. Existing rows are left untouched (INSERT IGNORE).
func (c *Client) InitProjectHealthRows(ctx context.Context, projectSysIDs []string, accountSysID string) error {
	for _, projectSysID := range projectSysIDs {
		_, err := c.db.ExecContext(ctx, `
			INSERT IGNORE INTO project_health_status (project_sys_id, account_sys_id, status)
			VALUES (?, ?, 'to_be_reviewed')`, projectSysID, accountSysID)
		if err != nil {
			return fmt.Errorf("risk: init health row for project %s: %w", projectSysID, err)
		}
	}
	return nil
}

func (c *Client) getRiskRowByID(ctx context.Context, riskID int) (projectRiskRow, error) {
	row := c.db.QueryRowContext(ctx, "SELECT "+projectRiskColumns+" FROM project_risk WHERE id = ?", riskID)
	r, err := scanProjectRiskRow(row)
	if err == sql.ErrNoRows {
		return projectRiskRow{}, errRecordNotFound
	}
	if err != nil {
		return projectRiskRow{}, fmt.Errorf("risk: query project_risk by id: %w", err)
	}
	return r, nil
}

func (c *Client) getRiskByID(ctx context.Context, riskID int) (*ProjectRisk, error) {
	riskRow, err := c.getRiskRowByID(ctx, riskID)
	if err != nil {
		return nil, err
	}
	actionItems, err := c.getActionItemsByRiskID(ctx, riskID)
	if err != nil {
		return nil, err
	}
	risk := mapRiskRowToRisk(riskRow, actionItems)
	return &risk, nil
}

// getHealthStatusByProject reads the row MarkProjectHealthy/RevertProjectHealth
// just wrote, scoped by the same (project_sys_id, account_sys_id) pair those
// writes key on -- project_sys_id alone is not guaranteed unique if a project
// is ever associated with more than one account's row, and reading unscoped
// could otherwise return a different account's row for the same project.
func (c *Client) getHealthStatusByProject(ctx context.Context, projectSysID, accountSysID string) (*HealthStatusRecord, error) {
	row := c.db.QueryRowContext(ctx,
		"SELECT "+healthStatusColumns+" FROM project_health_status WHERE project_sys_id = ? AND account_sys_id = ?",
		projectSysID, accountSysID)
	r, err := scanHealthStatusRow(row)
	if err == sql.ErrNoRows {
		return nil, errRecordNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("risk: query project_health_status by project: %w", err)
	}
	status := mapHealthStatusRowToHealthStatus(r)
	return &status, nil
}
