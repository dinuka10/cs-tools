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
	"fmt"
	"time"
)

// civilToString formats t as "YYYY-MM-DDTHH:MM:00", mirroring Ballerina
// civilToString (which always zeroes seconds — preserved here for output
// fidelity, not a rounding bug).
func civilToString(t time.Time) string {
	return fmt.Sprintf("%04d-%02d-%02dT%02d:%02d:00", t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute())
}

// dateToString formats t as "YYYY-MM-DD", mirroring Ballerina dateToString.
func dateToString(t time.Time) string {
	return fmt.Sprintf("%04d-%02d-%02d", t.Year(), t.Month(), t.Day())
}

func nullStringToPtr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	s := v.String
	return &s
}

func nullTimeToCivilStringPtr(v sql.NullTime) *string {
	if !v.Valid {
		return nil
	}
	s := civilToString(v.Time)
	return &s
}

func nullTimeToDateStringPtr(v sql.NullTime) *string {
	if !v.Valid {
		return nil
	}
	s := dateToString(v.Time)
	return &s
}

func mapRiskRowToRisk(row projectRiskRow, actionItems []RiskActionItem) ProjectRisk {
	if actionItems == nil {
		actionItems = []RiskActionItem{}
	}
	return ProjectRisk{
		ID:            row.ID,
		ProjectSysID:  row.ProjectSysID,
		AccountSysID:  row.AccountSysID,
		Status:        row.Status,
		OpenedComment: row.OpenedComment,
		OpenedByEmail: row.OpenedByEmail,
		OpenedOn:      civilToString(row.OpenedOn),
		ClosedComment: nullStringToPtr(row.ClosedComment),
		ClosedByEmail: nullStringToPtr(row.ClosedByEmail),
		ClosedOn:      nullTimeToCivilStringPtr(row.ClosedOn),
		ActionItems:   actionItems,
	}
}

func mapHealthStatusRowToHealthStatus(row healthStatusRow) HealthStatusRecord {
	return HealthStatusRecord{
		ID:              row.ID,
		ProjectSysID:    row.ProjectSysID,
		AccountSysID:    row.AccountSysID,
		Status:          row.Status,
		ReviewedByEmail: nullStringToPtr(row.ReviewedByEmail),
		ReviewedOn:      nullTimeToCivilStringPtr(row.ReviewedOn),
	}
}

func mapActionItemRowToActionItem(row actionItemRow) RiskActionItem {
	return RiskActionItem{
		ID:                row.ID,
		RiskID:            row.RiskID,
		ProjectSysID:      row.ProjectSysID,
		AccountSysID:      row.AccountSysID,
		Title:             row.Title,
		Description:       nullStringToPtr(row.Description),
		Priority:          row.Priority,
		Status:            row.Status,
		AssignedToEmail:   nullStringToPtr(row.AssignedToEmail),
		DueDate:           nullTimeToDateStringPtr(row.DueDate),
		ResolutionComment: nullStringToPtr(row.ResolutionComment),
		ResolvedByEmail:   nullStringToPtr(row.ResolvedByEmail),
		ResolvedOn:        nullTimeToCivilStringPtr(row.ResolvedOn),
		CreatedByEmail:    row.CreatedByEmail,
		CreatedOn:         civilToString(row.CreatedOn),
		UpdatedOn:         civilToString(row.UpdatedOn),
	}
}

func mapCommentRowToComment(row actionItemCommentRow) ActionItemComment {
	return ActionItemComment{
		ID:             row.ID,
		ActionItemID:   row.ActionItemID,
		Comment:        row.Comment,
		CreatedByEmail: row.CreatedByEmail,
		CreatedOn:      civilToString(row.CreatedOn),
	}
}
