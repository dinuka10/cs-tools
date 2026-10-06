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

package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
)

// snServicesStub serves ServiceNow's POST /services/search from services,
// paged by the request's offset/limit exactly as ServiceNow pages it. Each
// entry is a service sys_id mapped to its support group sys_id ("" for none).
func snServicesStub(services []snServiceFixture) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body snITServiceSearchPayload
		_ = json.NewDecoder(r.Body).Decode(&body)
		page := []map[string]any{}
		for i := body.Pagination.Offset; i < len(services) && i < body.Pagination.Offset+body.Pagination.Limit; i++ {
			svc := map[string]any{"id": services[i].sysid, "name": "svc"}
			if services[i].group != "" {
				svc["supportGroup"] = map[string]any{"id": services[i].group, "label": "Group"}
			}
			page = append(page, svc)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"services": page, "totalRecords": len(services)})
	}
}

type snServiceFixture struct{ sysid, group string }

// snCreateCapturingClient serves the lookup and POST /incidents, recording
// the create body and how many lookups were made.
func snCreateCapturingClient(t *testing.T, services []snServiceFixture, gotBody *map[string]any, lookups *int32) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	stub := snServicesStub(services)
	mux.HandleFunc("/services/search", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(lookups, 1)
		stub(w, r)
	})
	mux.HandleFunc("/incidents", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":"Incident created successfully.","incident":{"id":"` + testIncidentSysid + `","number":"INC0001","createdOn":"2026-01-01 00:00:00","createdBy":"engineer@example.com"}}`))
	})
	return mux
}

// DATA_SOURCE=servicenow: the group comes from ServiceNow's own service
// record, found by id even when it is not on the first page.
func TestSNCreateIncident_GroupFromTheServiceOnALaterPage(t *testing.T) {
	req := validCreateIncidentRequest()
	want := uuidToSysid(req.ServiceID)
	services := make([]snServiceFixture, 0, maxLimit+5)
	for i := 0; i < maxLimit+4; i++ {
		services = append(services, snServiceFixture{sysid: fmt.Sprintf("%032x", i+1), group: "0123456789abcdef0123456789abcdef"})
	}
	const group = "fedcba9876543210fedcba9876543210"
	services = append(services, snServiceFixture{sysid: want, group: group})

	var body map[string]any
	var lookups int32
	svc := NewServiceNowIncidentService(newTestSNClient(t, snCreateCapturingClient(t, services, &body, &lookups)), nil)
	if _, err := svc.CreateIncident(contextWithUserIDToken("token"), req); err != nil {
		t.Fatalf("CreateIncident: %v", err)
	}
	if body["assignmentGroupId"] != group {
		t.Errorf("assignmentGroupId sent to ServiceNow = %v, want the service's support group %s", body["assignmentGroupId"], group)
	}
	if lookups != 2 {
		t.Errorf("lookups = %d, want 2 (stop at the page holding the service)", lookups)
	}
}

// A service with no support group, or one ServiceNow does not list, is
// created unassigned rather than failing the create.
func TestSNCreateIncident_NoGroupLeavesItUnassigned(t *testing.T) {
	req := validCreateIncidentRequest()
	for name, services := range map[string][]snServiceFixture{
		"service has no support group": {{sysid: uuidToSysid(req.ServiceID)}},
		"service not listed":           {{sysid: "0123456789abcdef0123456789abcdef", group: "fedcba9876543210fedcba9876543210"}},
	} {
		t.Run(name, func(t *testing.T) {
			var body map[string]any
			var lookups int32
			svc := NewServiceNowIncidentService(newTestSNClient(t, snCreateCapturingClient(t, services, &body, &lookups)), nil)
			if _, err := svc.CreateIncident(contextWithUserIDToken("token"), req); err != nil {
				t.Fatalf("CreateIncident: %v", err)
			}
			if v, ok := body["assignmentGroupId"]; ok {
				t.Errorf("assignmentGroupId = %v, want none", v)
			}
		})
	}
}

// The dual-write mirror sends the group incidentService chose from Postgres
// and never looks one up in ServiceNow, so the two sides cannot disagree.
func TestSNIncidentMirror_SendsTheGivenGroupWithoutALookup(t *testing.T) {
	req := validCreateIncidentRequest()
	group := testSupportGroup
	req.AssignmentGroupID = &group
	services := []snServiceFixture{{sysid: uuidToSysid(req.ServiceID), group: "fedcba9876543210fedcba9876543210"}}

	var body map[string]any
	var lookups int32
	svc := NewServiceNowIncidentMirrorService(newTestSNClient(t, snCreateCapturingClient(t, services, &body, &lookups)))
	if _, err := svc.CreateIncident(contextWithUserIDToken("token"), req); err != nil {
		t.Fatalf("CreateIncident: %v", err)
	}
	if lookups != 0 {
		t.Errorf("lookups = %d, want 0", lookups)
	}
	if body["assignmentGroupId"] != uuidToSysid(testSupportGroup) {
		t.Errorf("assignmentGroupId = %v, want %s", body["assignmentGroupId"], uuidToSysid(testSupportGroup))
	}
}

// Running out of pages is not proof the service has no group -- it may be
// further on -- so the create fails rather than going out unassigned.
func TestSNCreateIncident_ExhaustedScanIsAnError(t *testing.T) {
	req := validCreateIncidentRequest()
	services := make([]snServiceFixture, 0, snServiceScanMaxPages*maxLimit+1)
	for i := 0; i < snServiceScanMaxPages*maxLimit; i++ {
		services = append(services, snServiceFixture{sysid: fmt.Sprintf("%032x", i+1), group: "0123456789abcdef0123456789abcdef"})
	}
	services = append(services, snServiceFixture{sysid: uuidToSysid(req.ServiceID), group: "fedcba9876543210fedcba9876543210"})

	var body map[string]any
	var lookups int32
	svc := NewServiceNowIncidentService(newTestSNClient(t, snCreateCapturingClient(t, services, &body, &lookups)), nil)
	if _, err := svc.CreateIncident(contextWithUserIDToken("token"), req); err == nil {
		t.Fatal("CreateIncident succeeded; an inconclusive scan must not create the incident unassigned")
	}
	if body != nil {
		t.Errorf("an incident was created: %v", body)
	}
	if lookups != snServiceScanMaxPages {
		t.Errorf("lookups = %d, want %d", lookups, snServiceScanMaxPages)
	}
}
