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

const actionItemColumns = "id, risk_id, project_sys_id, account_sys_id, title, description, priority, status, " +
	"assigned_to_email, due_date, resolution_comment, resolved_by_email, resolved_on, created_by_email, created_on, updated_on"

const actionItemCommentColumns = "id, action_item_id, comment, created_by_email, created_on"

// nilableString returns nil (to bind a SQL NULL) when s is nil, or *s
// otherwise — for optional string fields passed to Exec/Query.
func nilableString(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func scanActionItemRow(row interface{ Scan(...any) error }) (actionItemRow, error) {
	var r actionItemRow
	err := row.Scan(&r.ID, &r.RiskID, &r.ProjectSysID, &r.AccountSysID, &r.Title, &r.Description, &r.Priority,
		&r.Status, &r.AssignedToEmail, &r.DueDate, &r.ResolutionComment, &r.ResolvedByEmail, &r.ResolvedOn,
		&r.CreatedByEmail, &r.CreatedOn, &r.UpdatedOn)
	return r, err
}

// CreateActionItem creates a new action item on the given risk. The action
// item's project and account are taken from the risk row itself, not the
// request body, so a caller can't attach an item to a risk while giving it a
// different account/project (which would then surface under the wrong
// account in GetActionItemsByAccount, and silently block CloseProjectRisk
// for the real risk). Returns *ValidationError when the risk doesn't exist
// or is no longer open.
//
// Runs in a transaction that locks the risk row (SELECT ... FOR UPDATE),
// the same lock CloseProjectRisk takes: a plain, non-locking read here
// would let this insert race a concurrent close (read "open" -> close
// commits -> insert an "open" item onto the now-closed risk), which is
// exactly the interleaving CloseProjectRisk's own locking is meant to rule
// out.
func (c *Client) CreateActionItem(ctx context.Context, riskID int, payload CreateActionItemRequest, email string) (*RiskActionItem, error) {
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

	riskRow, err := scanProjectRiskRow(tx.QueryRowContext(ctx,
		"SELECT "+projectRiskColumns+" FROM project_risk WHERE id = ? FOR UPDATE", riskID))
	if err == sql.ErrNoRows {
		return nil, &ValidationError{Message: fmt.Sprintf("Risk not found: %d", riskID)}
	}
	if err != nil {
		return nil, fmt.Errorf("risk: query project_risk by id: %w", err)
	}
	if riskRow.Status != "open" {
		return nil, &ValidationError{Message: "Action items can only be added to open risks."}
	}

	execResult, err := tx.ExecContext(ctx, `
		INSERT INTO risk_action_item
			(risk_id, project_sys_id, account_sys_id, title, description, priority, status,
			 assigned_to_email, due_date, created_by_email)
		VALUES (?, ?, ?, ?, ?, ?, 'open', ?, ?, ?)`,
		riskID, riskRow.ProjectSysID, riskRow.AccountSysID, payload.Title, nilableString(payload.Description),
		payload.Priority, nilableString(payload.AssignedToEmail), payload.DueDate, email)
	if err != nil {
		return nil, fmt.Errorf("risk: insert risk_action_item: %w", err)
	}
	itemID, err := execResult.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("risk: read inserted action item id: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("risk: commit transaction: %w", err)
	}
	committed = true
	return c.getActionItemByID(ctx, int(itemID))
}

// UpdateActionItemStatus updates an action item's status. Returns
// *ValidationError when newStatus isn't one of the four valid statuses,
// when resolutionComment is required (status "resolved" or "cancelled")
// but missing/blank, or when reopening the item ("open"/"in_progress")
// would leave an active item on a risk that isn't open itself — the same
// "closed risk with an open item" state CloseProjectRisk is designed to
// prevent.
//
// The reopen check and the item update run in one transaction that locks
// the risk row (SELECT ... FOR UPDATE), the same lock CloseProjectRisk
// takes: without it, this could read the risk as "open", lose a race to a
// concurrent CloseProjectRisk (which only counts items not already
// resolved/cancelled), and then write "open" onto an item whose risk just
// closed.
func (c *Client) UpdateActionItemStatus(ctx context.Context, actionItemID int, newStatus string, resolutionComment *string, email string) (*RiskActionItem, error) {
	if newStatus != "open" && newStatus != "in_progress" && newStatus != "resolved" && newStatus != "cancelled" {
		return nil, &ValidationError{Message: fmt.Sprintf("Invalid status: %s", newStatus)}
	}
	if (newStatus == "resolved" || newStatus == "cancelled") && (resolutionComment == nil || strings.TrimSpace(*resolutionComment) == "") {
		return nil, &ValidationError{Message: "resolutionComment is required when status is 'resolved' or 'cancelled'"}
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

	row, err := scanActionItemRow(tx.QueryRowContext(ctx, "SELECT "+actionItemColumns+" FROM risk_action_item WHERE id = ?", actionItemID))
	if err == sql.ErrNoRows {
		return nil, errRecordNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("risk: query risk_action_item by id: %w", err)
	}

	if newStatus == "open" || newStatus == "in_progress" {
		riskRow, err := scanProjectRiskRow(tx.QueryRowContext(ctx,
			"SELECT "+projectRiskColumns+" FROM project_risk WHERE id = ? FOR UPDATE", row.RiskID))
		if err == sql.ErrNoRows {
			return nil, &ValidationError{Message: fmt.Sprintf("Risk not found: %d", row.RiskID)}
		}
		if err != nil {
			return nil, fmt.Errorf("risk: query project_risk by id: %w", err)
		}
		if riskRow.Status != "open" {
			return nil, &ValidationError{Message: "Cannot reopen an action item on a risk that is not open."}
		}
	}

	if newStatus == "resolved" || newStatus == "cancelled" {
		_, err = tx.ExecContext(ctx, `
			UPDATE risk_action_item
			SET status = ?, resolution_comment = ?, resolved_by_email = ?, resolved_on = NOW()
			WHERE id = ?`, newStatus, *resolutionComment, email, actionItemID)
		if err != nil {
			return nil, fmt.Errorf("risk: resolve/cancel action item: %w", err)
		}
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE risk_action_item SET status = ? WHERE id = ?`, newStatus, actionItemID)
		if err != nil {
			return nil, fmt.Errorf("risk: update action item status: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("risk: commit transaction: %w", err)
	}
	committed = true

	return c.getActionItemByID(ctx, actionItemID)
}

// UpdateActionItem updates an action item's details. Only allowed while its
// status is "open" or "in_progress"; returns *ValidationError otherwise.
func (c *Client) UpdateActionItem(ctx context.Context, actionItemID int, payload UpdateActionItemRequest) (*RiskActionItem, error) {
	row, err := c.getActionItemRowByID(ctx, actionItemID)
	if err != nil {
		return nil, err
	}

	if row.Status != "open" && row.Status != "in_progress" {
		return nil, &ValidationError{Message: fmt.Sprintf(
			"Cannot edit action item with status '%s'. Only 'open' or 'in_progress' items can be edited.", row.Status)}
	}

	_, err = c.db.ExecContext(ctx, `
		UPDATE risk_action_item
		SET title = ?, description = ?, priority = ?, assigned_to_email = ?, due_date = ?
		WHERE id = ?`,
		payload.Title, nilableString(payload.Description), payload.Priority, nilableString(payload.AssignedToEmail),
		nilableString(payload.DueDate), actionItemID)
	if err != nil {
		return nil, fmt.Errorf("risk: update action item: %w", err)
	}

	return c.getActionItemByID(ctx, actionItemID)
}

// GetActionItemsByRisk retrieves all action items for a risk, optionally
// filtered by status, newest first, enriched with comment counts.
func (c *Client) GetActionItemsByRisk(ctx context.Context, riskID int, statusFilter *string) ([]RiskActionItem, error) {
	query := "SELECT " + actionItemColumns + " FROM risk_action_item WHERE risk_id = ?"
	args := []any{riskID}
	if statusFilter != nil {
		query += " AND status = ?"
		args = append(args, *statusFilter)
	}
	query += " ORDER BY created_on DESC"

	items, err := c.queryActionItems(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return c.enrichWithCommentCounts(ctx, items)
}

// GetActionItemsByAccount retrieves all action items belonging to open
// risks under an account, optionally filtered by project and/or status,
// newest first, enriched with comment counts.
func (c *Client) GetActionItemsByAccount(ctx context.Context, accountSysID string, projectSysID, statusFilter *string) ([]RiskActionItem, error) {
	// Prefix every bare column name with the "ai" alias to match the joined query below.
	prefixed := make([]string, 0, len(strings.Split(actionItemColumns, ", ")))
	for _, col := range strings.Split(actionItemColumns, ", ") {
		prefixed = append(prefixed, "ai."+col)
	}

	query := "SELECT " + strings.Join(prefixed, ", ") + ` FROM risk_action_item ai
		INNER JOIN project_risk pr ON ai.risk_id = pr.id
		WHERE ai.account_sys_id = ? AND pr.status = 'open'`
	args := []any{accountSysID}

	if projectSysID != nil {
		query += " AND ai.project_sys_id = ?"
		args = append(args, *projectSysID)
	}
	if statusFilter != nil {
		query += " AND ai.status = ?"
		args = append(args, *statusFilter)
	}
	query += " ORDER BY ai.created_on DESC"

	items, err := c.queryActionItems(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return c.enrichWithCommentCounts(ctx, items)
}

func (c *Client) queryActionItems(ctx context.Context, query string, args ...any) ([]RiskActionItem, error) {
	rows, err := c.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("risk: query risk_action_item: %w", err)
	}
	defer rows.Close()

	items := []RiskActionItem{}
	for rows.Next() {
		r, err := scanActionItemRow(rows)
		if err != nil {
			return nil, fmt.Errorf("risk: scan risk_action_item row: %w", err)
		}
		items = append(items, mapActionItemRowToActionItem(r))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("risk: iterate risk_action_item rows: %w", err)
	}
	return items, nil
}

// CreateActionItemComment posts a comment on an action item. Returns
// *ValidationError if comment is blank.
func (c *Client) CreateActionItemComment(ctx context.Context, actionItemID int, comment, email string) (*ActionItemComment, error) {
	if strings.TrimSpace(comment) == "" {
		return nil, &ValidationError{Message: "Comment cannot be empty."}
	}

	execResult, err := c.db.ExecContext(ctx, `
		INSERT INTO action_item_comment (action_item_id, comment, created_by_email)
		VALUES (?, ?, ?)`, actionItemID, comment, email)
	if err != nil {
		return nil, fmt.Errorf("risk: insert action_item_comment: %w", err)
	}
	commentID, err := execResult.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("risk: read inserted comment id: %w", err)
	}
	return c.getCommentByID(ctx, int(commentID))
}

// GetActionItemComments retrieves all comments for an action item, oldest
// first.
func (c *Client) GetActionItemComments(ctx context.Context, actionItemID int) ([]ActionItemComment, error) {
	rows, err := c.db.QueryContext(ctx,
		"SELECT "+actionItemCommentColumns+" FROM action_item_comment WHERE action_item_id = ? ORDER BY created_on ASC",
		actionItemID)
	if err != nil {
		return nil, fmt.Errorf("risk: query action_item_comment: %w", err)
	}
	defer rows.Close()

	comments := []ActionItemComment{}
	for rows.Next() {
		var r actionItemCommentRow
		if err := rows.Scan(&r.ID, &r.ActionItemID, &r.Comment, &r.CreatedByEmail, &r.CreatedOn); err != nil {
			return nil, fmt.Errorf("risk: scan action_item_comment row: %w", err)
		}
		comments = append(comments, mapCommentRowToComment(r))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("risk: iterate action_item_comment rows: %w", err)
	}
	return comments, nil
}

func (c *Client) getActionItemRowByID(ctx context.Context, actionItemID int) (actionItemRow, error) {
	row := c.db.QueryRowContext(ctx, "SELECT "+actionItemColumns+" FROM risk_action_item WHERE id = ?", actionItemID)
	r, err := scanActionItemRow(row)
	if err == sql.ErrNoRows {
		return actionItemRow{}, errRecordNotFound
	}
	if err != nil {
		return actionItemRow{}, fmt.Errorf("risk: query risk_action_item by id: %w", err)
	}
	return r, nil
}

func (c *Client) getActionItemByID(ctx context.Context, actionItemID int) (*RiskActionItem, error) {
	row, err := c.getActionItemRowByID(ctx, actionItemID)
	if err != nil {
		return nil, err
	}
	item := mapActionItemRowToActionItem(row)
	return &item, nil
}

func (c *Client) getActionItemsByRiskID(ctx context.Context, riskID int) ([]RiskActionItem, error) {
	items, err := c.queryActionItems(ctx,
		"SELECT "+actionItemColumns+" FROM risk_action_item WHERE risk_id = ? ORDER BY created_on DESC", riskID)
	if err != nil {
		return nil, err
	}
	return c.enrichWithCommentCounts(ctx, items)
}

// enrichWithCommentCounts populates CommentCount on each item via a single
// batch query, mirroring the Ballerina source's getCommentCountsByItemIds.
func (c *Client) enrichWithCommentCounts(ctx context.Context, items []RiskActionItem) ([]RiskActionItem, error) {
	if len(items) == 0 {
		return items, nil
	}

	ids := make([]any, len(items))
	placeholders := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.ID
		placeholders[i] = "?"
	}

	// #nosec G202 -- placeholders is a slice of literal "?" strings, one per
	// item, never derived from caller-supplied data; every actual value is
	// passed as a parameterized arg via ids... below. Standard
	// database/sql pattern for a variadic IN (...) clause.
	rows, err := c.db.QueryContext(ctx,
		"SELECT action_item_id, COUNT(*) as comment_count FROM action_item_comment WHERE action_item_id IN ("+
			strings.Join(placeholders, ", ")+") GROUP BY action_item_id", ids...)
	if err != nil {
		return nil, fmt.Errorf("risk: query comment counts: %w", err)
	}
	defer rows.Close()

	counts := make(map[int]int)
	for rows.Next() {
		var actionItemID, count int
		if err := rows.Scan(&actionItemID, &count); err != nil {
			return nil, fmt.Errorf("risk: scan comment count row: %w", err)
		}
		counts[actionItemID] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("risk: iterate comment count rows: %w", err)
	}

	enriched := make([]RiskActionItem, len(items))
	for i, item := range items {
		item.CommentCount = counts[item.ID]
		enriched[i] = item
	}
	return enriched, nil
}

func (c *Client) getCommentByID(ctx context.Context, commentID int) (*ActionItemComment, error) {
	row := c.db.QueryRowContext(ctx, "SELECT "+actionItemCommentColumns+" FROM action_item_comment WHERE id = ?", commentID)
	var r actionItemCommentRow
	err := row.Scan(&r.ID, &r.ActionItemID, &r.Comment, &r.CreatedByEmail, &r.CreatedOn)
	if err == sql.ErrNoRows {
		return nil, errRecordNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("risk: query action_item_comment by id: %w", err)
	}
	comment := mapCommentRowToComment(r)
	return &comment, nil
}
