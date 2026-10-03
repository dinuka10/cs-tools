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

package dto

import (
	"testing"

	"github.com/wso2-open-operations/cs-tools/apps/customer-portal/backend-v2/internal/entity"
)

// The dashboard's outstanding-engagements chart matches engagement type
// buckets on the LABEL, against "Consultancy"/"Onboarding"/"Migration"/
// "Follow Up"/"New Feature Improvement" (features/dashboard/constants/dashboard.ts,
// OUTSTANDING_ENGAGEMENTS_CATEGORY_CHART_DATA). The Postgres data source
// returns the raw enum label (UPPER_SNAKE) as both id and label -- without
// normalization every bucket misses that match and silently drops out of
// both the chart and its total, the same class of bug
// TestNormalizeCaseSeverityChoices_PostgresEnumLabels guards for severity.
func TestMapProjectCaseStats_NormalizesEngagementTypeChoices(t *testing.T) {
	resp := entity.ProjectCaseStatsResponse{
		EngagementTypeCount: []entity.ChoiceListItem{
			{ID: "NEW_FEATURE_IMPROVEMENT", Label: "NEW_FEATURE_IMPROVEMENT", Count: intPtr(2)},
		},
		OutstandingEngagementTypeCount: []entity.ChoiceListItem{
			{ID: "FOLLOW_UP", Label: "FOLLOW_UP", Count: intPtr(3)},
		},
	}

	got := MapProjectCaseStats(resp)

	if len(got.EngagementTypeCount) != 1 || got.EngagementTypeCount[0].Label != "New Feature Improvement" {
		t.Fatalf("EngagementTypeCount not normalized: %+v", got.EngagementTypeCount)
	}
	if len(got.OutstandingEngagementTypeCount) != 1 || got.OutstandingEngagementTypeCount[0].Label != "Follow Up" {
		t.Fatalf("OutstandingEngagementTypeCount not normalized: %+v", got.OutstandingEngagementTypeCount)
	}
}

// GET /projects/{id}/filters' changeRequestStates/changeRequestImpacts fed
// ChangeRequestsPage.tsx's State/Impact filter dropdowns directly. On the
// Postgres data source these carried the raw enum label as id
// (e.g. {"id":"ROLLBACK"}), never run through the normalizer every sibling
// field on this same response already uses -- so filters.stateIds?.map(Number)
// converted every selection to NaN, which reached the search request as
// null instead of a real state key. Also verifies the three internal
// pre-approval states (New/Assess/Authorize) are excluded from the list
// entirely, matching what ChangeRequestsPage.tsx's own filter panel renders
// unfiltered from this same response.
func TestMapProjectFilterOptions_NormalizesChangeRequestChoicesAndExcludesInternalStates(t *testing.T) {
	resp := entity.ProjectMetadataResponse{
		ChangeRequestStates: []entity.ChoiceListItem{
			{ID: "NEW", Label: "NEW"},
			{ID: "ASSESS", Label: "ASSESS"},
			{ID: "AUTHORIZE", Label: "AUTHORIZE"},
			{ID: "ROLLBACK", Label: "ROLLBACK"},
			{ID: "CLOSED", Label: "CLOSED"},
		},
		ChangeRequestImpacts: []entity.ChoiceListItem{
			{ID: "HIGH", Label: "HIGH"},
		},
	}

	got := MapProjectFilterOptions(resp)

	if len(got.ChangeRequestStates) != 2 {
		t.Fatalf("ChangeRequestStates = %+v, want exactly Rollback and Closed (New/Assess/Authorize excluded)", got.ChangeRequestStates)
	}
	if got.ChangeRequestStates[0].ID != "2" || got.ChangeRequestStates[0].Label != "Rollback" {
		t.Errorf("ChangeRequestStates[0] = %+v, want {ID: \"2\", Label: \"Rollback\"}", got.ChangeRequestStates[0])
	}
	if got.ChangeRequestStates[1].ID != "3" || got.ChangeRequestStates[1].Label != "Closed" {
		t.Errorf("ChangeRequestStates[1] = %+v, want {ID: \"3\", Label: \"Closed\"}", got.ChangeRequestStates[1])
	}
	if len(got.ChangeRequestImpacts) != 1 || got.ChangeRequestImpacts[0].ID != "1" || got.ChangeRequestImpacts[0].Label != "High" {
		t.Errorf("ChangeRequestImpacts = %+v, want [{ID: \"1\", Label: \"High\"}]", got.ChangeRequestImpacts)
	}
}

// The exclusion above must catch an internal state under either data
// source's own shape: ServiceNow's real numeric id ("-3") with a
// display-cased label, and Postgres's raw UPPER_SNAKE label with no
// matching id. Missing either would let that one data source's internal
// state leak into the customer-facing filter panel.
func TestMapProjectFilterOptions_ExcludesInternalChangeRequestStatesByIDOrLabel(t *testing.T) {
	resp := entity.ProjectMetadataResponse{
		ChangeRequestStates: []entity.ChoiceListItem{
			{ID: "-3", Label: "New"},        // ServiceNow: real numeric id, display-cased label
			{ID: "-4", Label: "Assess"},      // ServiceNow
			{ID: "-5", Label: "Authorize"},   // ServiceNow
			{ID: "AUTHORIZE", Label: "AUTHORIZE"}, // Postgres: no id, UPPER_SNAKE label
			{ID: "CLOSED", Label: "CLOSED"},
		},
	}

	got := MapProjectFilterOptions(resp)

	if len(got.ChangeRequestStates) != 1 || got.ChangeRequestStates[0].Label != "Closed" {
		t.Fatalf("ChangeRequestStates = %+v, want exactly Closed", got.ChangeRequestStates)
	}
}

// TestMapProjectFilterOptions_ExposesResolutionCodesAndCauses is the
// regression test for a real, reported bug: closing a case requires
// resolutionCode/cause/closeNotes, but the webapp had no choice lists to
// build a close dialog from at all -- GET /projects/{id}/filters simply
// never carried either field. Confirms both now pass through unchanged.
func TestMapProjectFilterOptions_ExposesResolutionCodesAndCauses(t *testing.T) {
	resp := entity.ProjectMetadataResponse{
		ResolutionCodes: []entity.ChoiceListItem{{ID: "SOLVED_WORKAROUND_PROVIDED", Label: "Solved Workaround Provided"}},
		Causes:          []entity.ChoiceListItem{{ID: "PRODUCT_BUG", Label: "Product Bug"}},
	}

	got := MapProjectFilterOptions(resp)

	if len(got.ResolutionCodes) != 1 || got.ResolutionCodes[0].ID != "SOLVED_WORKAROUND_PROVIDED" {
		t.Fatalf("ResolutionCodes = %+v", got.ResolutionCodes)
	}
	if len(got.Causes) != 1 || got.Causes[0].ID != "PRODUCT_BUG" {
		t.Fatalf("Causes = %+v", got.Causes)
	}
}

// GET /projects/{id}/stats/change-requests feeds the Operations page's
// Upcoming Changes / Action Required Changes cards, which find their counts by
// display label ("Scheduled", "Customer Approval", "Customer Review"). The
// Postgres data source returns the raw enum as both id and label, so without
// normalization "SCHEDULED" never matched and the card showed "--" while the
// change-request list beside it showed four Scheduled changes.
func TestMapProjectChangeRequestStats_NormalizesStateCountLabels(t *testing.T) {
	four, zero := 4, 0
	resp := entity.ProjectChangeRequestStatsResponse{
		TotalCount: 134,
		StateCount: []entity.ChoiceListItem{
			{ID: "CUSTOMER_APPROVAL", Label: "CUSTOMER_APPROVAL", Count: &zero},
			{ID: "SCHEDULED", Label: "SCHEDULED", Count: &four},
			{ID: "CUSTOMER_REVIEW", Label: "CUSTOMER_REVIEW", Count: &zero},
		},
	}

	got := MapProjectChangeRequestStats(resp)

	want := []struct{ id, label string }{
		{"5", "Customer Approval"},
		{"-2", "Scheduled"},
		{"1", "Customer Review"},
	}
	if len(got.StateCount) != len(want) {
		t.Fatalf("StateCount = %+v, want %d entries", got.StateCount, len(want))
	}
	for i, w := range want {
		if got.StateCount[i].ID != w.id || got.StateCount[i].Label != w.label {
			t.Errorf("StateCount[%d] = {%q, %q}, want {%q, %q}",
				i, got.StateCount[i].ID, got.StateCount[i].Label, w.id, w.label)
		}
	}
	if got.StateCount[1].Count == nil || *got.StateCount[1].Count != 4 {
		t.Errorf("Scheduled count = %v, want 4 (counts must survive normalization)", got.StateCount[1].Count)
	}
}
