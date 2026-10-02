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
	"database/sql"
	"time"
)

// ----- request payloads (mirror Ballerina modules/risk/types.bal) -----

// OpenRiskRequest is the payload for opening a new risk on a project.
type OpenRiskRequest struct {
	AccountSysID string `json:"accountSysId"`
	Comment      string `json:"comment"`
}

// CloseRiskRequest is the payload for closing an open risk.
type CloseRiskRequest struct {
	Comment string `json:"comment"`
}

// MarkHealthyRequest is the payload for marking a project healthy.
type MarkHealthyRequest struct {
	AccountSysID string  `json:"accountSysId"`
	Comment      *string `json:"comment"`
}

// RevertHealthRequest is the payload for reverting a project's health
// status back to "to_be_reviewed".
type RevertHealthRequest struct {
	AccountSysID string `json:"accountSysId"`
}

// CreateActionItemRequest is the payload for creating a new action item on
// an open risk.
type CreateActionItemRequest struct {
	Title           string  `json:"title"`
	Description     *string `json:"description"`
	Priority        string  `json:"priority"`
	AssignedToEmail *string `json:"assignedToEmail"`
	DueDate         string  `json:"dueDate"`
	ProjectSysID    string  `json:"projectSysId"`
	AccountSysID    string  `json:"accountSysId"`
}

// UpdateActionItemStatusRequest is the payload for updating an action
// item's status.
type UpdateActionItemStatusRequest struct {
	Status            string  `json:"status"`
	ResolutionComment *string `json:"resolutionComment"`
}

// UpdateActionItemRequest is the payload for updating an action item's
// details.
type UpdateActionItemRequest struct {
	Title           string  `json:"title"`
	Description     *string `json:"description"`
	Priority        string  `json:"priority"`
	AssignedToEmail *string `json:"assignedToEmail"`
	DueDate         *string `json:"dueDate"`
}

// CreateCommentRequest is the payload for posting a comment on an action
// item.
type CreateCommentRequest struct {
	Comment string `json:"comment"`
}

// InitHealthTrackingRequest is the payload for initialising health-tracking
// rows for a list of projects under an account.
type InitHealthTrackingRequest struct {
	ProjectSysIDs []string `json:"projectSysIds"`
}

// ----- API response types (mirror Ballerina modules/risk/types.bal) -----

// RiskActionItem is an action item attached to a project risk.
type RiskActionItem struct {
	ID                int     `json:"id"`
	RiskID            int     `json:"riskId"`
	ProjectSysID      string  `json:"projectSysId"`
	AccountSysID      string  `json:"accountSysId"`
	Title             string  `json:"title"`
	Description       *string `json:"description"`
	Priority          string  `json:"priority"`
	Status            string  `json:"status"`
	AssignedToEmail   *string `json:"assignedToEmail"`
	DueDate           *string `json:"dueDate"`
	ResolutionComment *string `json:"resolutionComment"`
	ResolvedByEmail   *string `json:"resolvedByEmail"`
	ResolvedOn        *string `json:"resolvedOn"`
	CreatedByEmail    string  `json:"createdByEmail"`
	CreatedOn         string  `json:"createdOn"`
	UpdatedOn         string  `json:"updatedOn"`
	CommentCount      int     `json:"commentCount"`
}

// ProjectRisk is a project's risk record, open or closed, together with its
// action items.
type ProjectRisk struct {
	ID            int              `json:"id"`
	ProjectSysID  string           `json:"projectSysId"`
	AccountSysID  string           `json:"accountSysId"`
	Status        string           `json:"status"`
	OpenedComment string           `json:"openedComment"`
	OpenedByEmail string           `json:"openedByEmail"`
	OpenedOn      string           `json:"openedOn"`
	ClosedComment *string          `json:"closedComment"`
	ClosedByEmail *string          `json:"closedByEmail"`
	ClosedOn      *string          `json:"closedOn"`
	ActionItems   []RiskActionItem `json:"actionItems"`
}

// HealthStatusRecord is a project's current health-review status.
type HealthStatusRecord struct {
	ID              int     `json:"id"`
	ProjectSysID    string  `json:"projectSysId"`
	AccountSysID    string  `json:"accountSysId"`
	Status          string  `json:"status"`
	ReviewedByEmail *string `json:"reviewedByEmail"`
	ReviewedOn      *string `json:"reviewedOn"`
}

// ProjectHealthStatus is a project's health status together with its
// currently open risk, if any.
type ProjectHealthStatus struct {
	ProjectSysID string             `json:"projectSysId"`
	HealthStatus HealthStatusRecord `json:"healthStatus"`
	OpenRisk     *ProjectRisk       `json:"openRisk"`
}

// HealthSummary is an account's aggregated overall health status, derived
// from its projects' individual health statuses.
type HealthSummary struct {
	AccountSysID  string `json:"accountSysId"`
	OverallStatus string `json:"overallStatus"`
}

// ActionItemComment is a comment posted on an action item.
type ActionItemComment struct {
	ID             int    `json:"id"`
	ActionItemID   int    `json:"actionItemId"`
	Comment        string `json:"comment"`
	CreatedByEmail string `json:"createdByEmail"`
	CreatedOn      string `json:"createdOn"`
}

// ----- internal DB row shapes (unexported: mirror the Ballerina *Row types) -----

type projectRiskRow struct {
	ID            int
	ProjectSysID  string
	AccountSysID  string
	Status        string
	OpenedComment string
	OpenedByEmail string
	OpenedOn      time.Time
	ClosedComment sql.NullString
	ClosedByEmail sql.NullString
	ClosedOn      sql.NullTime
}

type healthStatusRow struct {
	ID              int
	ProjectSysID    string
	AccountSysID    string
	Status          string
	ReviewedByEmail sql.NullString
	ReviewedOn      sql.NullTime
}

type actionItemRow struct {
	ID                int
	RiskID            int
	ProjectSysID      string
	AccountSysID      string
	Title             string
	Description       sql.NullString
	Priority          string
	Status            string
	AssignedToEmail   sql.NullString
	DueDate           sql.NullTime
	ResolutionComment sql.NullString
	ResolvedByEmail   sql.NullString
	ResolvedOn        sql.NullTime
	CreatedByEmail    string
	CreatedOn         time.Time
	UpdatedOn         time.Time
}

type actionItemCommentRow struct {
	ID             int
	ActionItemID   int
	Comment        string
	CreatedByEmail string
	CreatedOn      time.Time
}
