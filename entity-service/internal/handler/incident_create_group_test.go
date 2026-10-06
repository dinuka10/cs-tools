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

package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// An incident's assignment group is its service's support group, chosen by
// the incident service. A create that tries to send one is refused before the
// service runs (NewIncidentHandler(nil) would panic if it got that far), so a
// client that still sends it finds out at once instead of being silently
// overridden.
func TestCreateIncident_RejectsAnAssignmentGroup(t *testing.T) {
	h := NewIncidentHandler(nil)
	body := `{"subject":"s","category":"INQUIRY","serviceId":"11111111-1111-4111-8111-111111111111",` +
		`"impact":"LOW","urgency":"LOW","callerId":"22222222-2222-4222-8222-222222222222",` +
		`"assignmentGroupId":"33333333-3333-4333-8333-333333333333"}`
	rec := httptest.NewRecorder()
	h.CreateIncident(rec, httptest.NewRequest(http.MethodPost, "/incidents", strings.NewReader(body)))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "assignmentGroupId") {
		t.Errorf("body = %s, want it to name assignmentGroupId", rec.Body.String())
	}
}
