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
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/wso2-open-operations/cs-tools/apps/csm-portal/backend/internal/scim"
)

// testAccessConfigForCSMRoles mirrors testAccessConfig, but with dummy token
// role names carrying the "app-csm-" prefix SCIM roles actually use -- so
// withPortalRoles' own prefix filter has something real to match against.
func testAccessConfigForCSMRoles() AccessConfig {
	return AccessConfig{
		Viewer:               []string{"app-csm-test-viewer"},
		Escalator:            []string{"app-csm-test-escalator"},
		AttachmentDownloader: []string{"app-csm-test-attachment-downloader"},
		UsageMetricsViewer:   []string{"app-csm-test-usage-metrics-viewer"},
		CsEngineer:           []string{"app-csm-test-cs-engineer"},
		Admin:                []string{"app-csm-test-admin"},
		TimecardApprover:     []string{"app-csm-test-timecard-approver"},
		DashboardDesigner:    []string{"app-csm-test-dashboard-designer"},
	}
}

// TestGetUser_PortalRoles_ReplacesEntityRolesForInternalUser: an internal
// (WSO2 staff) profile's roles come from SCIM's role assignment translated
// through AccessGuard.RolesFor, not entity-service's own role vocabulary --
// the two are unrelated vocabularies, and entity-service's reads as though
// most of the staff member's real access were missing.
func TestGetUser_PortalRoles_ReplacesEntityRolesForInternalUser(t *testing.T) {
	const id = "11111111-1111-1111-1111-111111111111"
	var gotEmail string
	h := NewUsersHandler(&mockSCIMClient{
		searchUserFn: func(_ context.Context, email string) (*scim.UserInfo, error) {
			gotEmail = email
			return &scim.UserInfo{Roles: []string{
				"app-csm-test-admin", "some-other-app-role",
			}}, nil
		},
	}, &mockEntityUserClient{
		getUserFn: func(_ context.Context, _ string) ([]byte, error) {
			return []byte(`{"id":"` + id + `","email":"staff@example.com","userType":"internal","roles":["admin","internal"]}`), nil
		},
	}, testDirectory(t), false, nil).WithAccessGuard(NewAccessGuard(testAccessConfigForCSMRoles()))

	r := withUser(httptest.NewRequest(http.MethodGet, "/users/"+id, nil))
	r.SetPathValue("id", id)
	w := httptest.NewRecorder()
	h.GetUser(w, r)

	assertStatus(t, w, http.StatusOK)
	if gotEmail != "staff@example.com" {
		t.Errorf("SearchUser called with email %q, want the profile's email", gotEmail)
	}
	got := decodeJSON[struct {
		Roles []string `json:"roles"`
	}](t, w)
	if !reflect.DeepEqual(got.Roles, []string{"admin"}) {
		t.Errorf("roles = %v, want [admin] (entity-service's own [admin internal] replaced)", got.Roles)
	}
}

// TestGetUser_PortalRoles_SkippedForExternalUser: an external contact's roles
// keep coming from entity-service -- there is no Asgardeo/SCIM role
// assignment to translate for a customer/partner contact.
func TestGetUser_PortalRoles_SkippedForExternalUser(t *testing.T) {
	const id = "11111111-1111-1111-1111-111111111111"
	called := false
	h := NewUsersHandler(&mockSCIMClient{
		searchUserFn: func(_ context.Context, _ string) (*scim.UserInfo, error) {
			called = true
			return &scim.UserInfo{Roles: []string{"app-csm-test-admin"}}, nil
		},
	}, &mockEntityUserClient{
		getUserFn: func(_ context.Context, _ string) ([]byte, error) {
			return []byte(`{"id":"` + id + `","email":"contact@example.com","userType":"external","roles":["customer"]}`), nil
		},
	}, testDirectory(t), false, nil).WithAccessGuard(NewAccessGuard(testAccessConfigForCSMRoles()))

	r := withUser(httptest.NewRequest(http.MethodGet, "/users/"+id, nil))
	r.SetPathValue("id", id)
	w := httptest.NewRecorder()
	h.GetUser(w, r)

	assertStatus(t, w, http.StatusOK)
	if called {
		t.Error("SearchUser was called for an external user, want no SCIM internal lookup")
	}
	got := decodeJSON[struct {
		Roles []string `json:"roles"`
	}](t, w)
	if !reflect.DeepEqual(got.Roles, []string{"customer"}) {
		t.Errorf("roles = %v, want entity-service's own [customer] left unchanged", got.Roles)
	}
}

// TestGetUser_PortalRoles_SkippedWhenAccessGuardNotWired: every existing
// caller/test that constructs a UsersHandler without WithAccessGuard must see
// no behavior change -- entity-service's own roles pass through untouched.
func TestGetUser_PortalRoles_SkippedWhenAccessGuardNotWired(t *testing.T) {
	const id = "11111111-1111-1111-1111-111111111111"
	called := false
	h := NewUsersHandler(&mockSCIMClient{
		searchUserFn: func(_ context.Context, _ string) (*scim.UserInfo, error) {
			called = true
			return &scim.UserInfo{Roles: []string{"app-csm-test-admin"}}, nil
		},
	}, &mockEntityUserClient{
		getUserFn: func(_ context.Context, _ string) ([]byte, error) {
			return []byte(`{"id":"` + id + `","email":"staff@example.com","userType":"internal","roles":["admin"]}`), nil
		},
	}, testDirectory(t), false, nil)

	r := withUser(httptest.NewRequest(http.MethodGet, "/users/"+id, nil))
	r.SetPathValue("id", id)
	w := httptest.NewRecorder()
	h.GetUser(w, r)

	assertStatus(t, w, http.StatusOK)
	if called {
		t.Error("SearchUser was called with no AccessGuard wired, want no SCIM lookup at all")
	}
	got := decodeJSON[struct {
		Roles []string `json:"roles"`
	}](t, w)
	if !reflect.DeepEqual(got.Roles, []string{"admin"}) {
		t.Errorf("roles = %v, want entity-service's own [admin] left unchanged", got.Roles)
	}
}

// TestGetUser_PortalRoles_FailureDoesNotFailTheRequest: a SCIM error must not
// turn a 200 into an error response -- this enrichment is best-effort, same
// as the external-account and teams enrichments on this same profile.
func TestGetUser_PortalRoles_FailureDoesNotFailTheRequest(t *testing.T) {
	const id = "11111111-1111-1111-1111-111111111111"
	h := NewUsersHandler(&mockSCIMClient{
		searchUserFn: func(_ context.Context, _ string) (*scim.UserInfo, error) {
			return nil, errors.New("scim unavailable")
		},
	}, &mockEntityUserClient{
		getUserFn: func(_ context.Context, _ string) ([]byte, error) {
			return []byte(`{"id":"` + id + `","email":"staff@example.com","userType":"internal","roles":["admin"]}`), nil
		},
	}, testDirectory(t), false, nil).WithAccessGuard(NewAccessGuard(testAccessConfigForCSMRoles()))

	r := withUser(httptest.NewRequest(http.MethodGet, "/users/"+id, nil))
	r.SetPathValue("id", id)
	w := httptest.NewRecorder()
	h.GetUser(w, r)

	assertStatus(t, w, http.StatusOK)
	got := decodeJSON[struct {
		Roles []string `json:"roles"`
	}](t, w)
	if !reflect.DeepEqual(got.Roles, []string{"admin"}) {
		t.Errorf("roles = %v, want entity-service's own [admin] left unchanged despite the SCIM failure", got.Roles)
	}
}
