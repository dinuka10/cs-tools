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

// Regression guard for the create-time assignment group: the CSM Portal's
// create form sends its "Assignment group" picker as
// CreateChangeRequestRequest.GroupID, and both Postgres create paths
// (CreateChangeRequest and CreateChangeRequestFromServiceNow) used to drop it
// on the floor -- the change request read back with no AssignedTeam. Skipped
// without CHANGE_REQUEST_TEST_DSN, same as change_request_repo_integration_test.go,
// whose seededGroupID/unknownGroupID this reuses.
//
//	CHANGE_REQUEST_TEST_DSN=postgres://... go test ./internal/repository/ -run ChangeRequestCreateIntegration

package repository_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wso2-open-operations/cs-tools/entity-service/internal/apierror"
	"github.com/wso2-open-operations/cs-tools/entity-service/internal/domain"
	"github.com/wso2-open-operations/cs-tools/entity-service/internal/repository"
)

const (
	crCreateSubject = "cr-create-assignment-group integration test"
	// crCreateSNID/crCreateSNNumber stand in for the identity ServiceNow
	// would have returned on the dual-write SN-first path.
	crCreateSNID     = "36666666-0000-0000-0000-0000000000c1"
	crCreateSNNumber = "CRAGTEST001"
)

// crCreateType returns a pointer to t -- a type is mandatory on every create.
func crCreateType(t domain.ChangeRequestType) *domain.ChangeRequestType { return &t }

func changeRequestCreatePool(t *testing.T) *repository.Scoped {
	t.Helper()
	dsn := os.Getenv("CHANGE_REQUEST_TEST_DSN")
	if dsn == "" {
		t.Skip("CHANGE_REQUEST_TEST_DSN not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	scoped := repository.NewScoped(pool)

	sys := repository.WithSystemIdentity(context.Background())
	cleanup := func() {
		_, _ = scoped.Exec(sys, `DELETE FROM work_item WHERE subject = $1 OR id = $2`, crCreateSubject, crCreateSNID)
	}
	cleanup()
	t.Cleanup(cleanup)
	return scoped
}

// assertChangeRequestAssignedTeam checks work_item.assignment_group_id
// directly and through GetChangeRequestByID (the read the portal's detail
// page actually makes). want == "" means no assignment group at all.
func assertChangeRequestAssignedTeam(t *testing.T, scoped *repository.Scoped, repo repository.ChangeRequestRepository, id, want string) {
	t.Helper()
	sys := repository.WithSystemIdentity(context.Background())

	var got *string
	if err := scoped.QueryRow(sys, `SELECT assignment_group_id::TEXT FROM work_item WHERE id = $1`, id).Scan(&got); err != nil {
		t.Fatalf("read back assignment_group_id: %v", err)
	}
	if want == "" {
		if got != nil {
			t.Fatalf("work_item.assignment_group_id = %q, want NULL", *got)
		}
	} else if got == nil || *got != want {
		t.Fatalf("work_item.assignment_group_id = %v, want %q", got, want)
	}

	cr, err := repo.GetChangeRequestByID(sys, id)
	if err != nil {
		t.Fatalf("GetChangeRequestByID: %v", err)
	}
	if want == "" {
		if cr.AssignedTeam != nil {
			t.Fatalf("GetChangeRequestByID AssignedTeam = %+v, want nil", cr.AssignedTeam)
		}
		return
	}
	if cr.AssignedTeam == nil || cr.AssignedTeam.ID != want {
		t.Fatalf("GetChangeRequestByID AssignedTeam = %+v, want ID %q", cr.AssignedTeam, want)
	}
	if cr.AssignedTeam.Name == "" {
		t.Fatalf("GetChangeRequestByID AssignedTeam.Name is empty, want the seeded group's name")
	}
}

func TestChangeRequestCreateIntegration_PortalPersistsAssignmentGroup(t *testing.T) {
	scoped := changeRequestCreatePool(t)
	repo := repository.NewChangeRequestRepository(scoped)
	sys := repository.WithSystemIdentity(context.Background())

	groupID := seededGroupID
	resp, err := repo.CreateChangeRequest(sys, domain.CreateChangeRequestRequest{
		Subject: crCreateSubject,
		Type:    crCreateType(domain.ChangeRequestTypeNormal),
		GroupID: &groupID,
	}, "cr-create-test@test.local")
	if err != nil {
		t.Fatalf("CreateChangeRequest: %v", err)
	}
	assertChangeRequestAssignedTeam(t, scoped, repo, resp.ChangeRequest.ID, groupID)
}

func TestChangeRequestCreateIntegration_PortalWithoutAssignmentGroupLeavesItNull(t *testing.T) {
	scoped := changeRequestCreatePool(t)
	repo := repository.NewChangeRequestRepository(scoped)
	sys := repository.WithSystemIdentity(context.Background())

	resp, err := repo.CreateChangeRequest(sys, domain.CreateChangeRequestRequest{Subject: crCreateSubject, Type: crCreateType(domain.ChangeRequestTypeNormal)}, "cr-create-test@test.local")
	if err != nil {
		t.Fatalf("CreateChangeRequest: %v", err)
	}
	assertChangeRequestAssignedTeam(t, scoped, repo, resp.ChangeRequest.ID, "")
}

func TestChangeRequestCreateIntegration_PortalUnknownAssignmentGroupIsValidationError(t *testing.T) {
	scoped := changeRequestCreatePool(t)
	repo := repository.NewChangeRequestRepository(scoped)
	sys := repository.WithSystemIdentity(context.Background())

	groupID := unknownGroupID
	_, err := repo.CreateChangeRequest(sys, domain.CreateChangeRequestRequest{
		Subject: crCreateSubject,
		Type:    crCreateType(domain.ChangeRequestTypeNormal),
		GroupID: &groupID,
	}, "cr-create-test@test.local")
	var ve *apierror.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("CreateChangeRequest(unknown groupId) err = %v (%T), want *apierror.ValidationError", err, err)
	}
}

func TestChangeRequestCreateIntegration_FromServiceNowPersistsAssignmentGroup(t *testing.T) {
	scoped := changeRequestCreatePool(t)
	repo := repository.NewChangeRequestRepository(scoped)
	sys := repository.WithSystemIdentity(context.Background())

	groupID := seededGroupID
	resp, err := repo.CreateChangeRequestFromServiceNow(sys, domain.CreateChangeRequestRequest{
		Subject: crCreateSubject,
		Type:    crCreateType(domain.ChangeRequestTypeNormal),
		GroupID: &groupID,
	}, crCreateSNID, crCreateSNNumber, "cr-create-test@test.local")
	if err != nil {
		t.Fatalf("CreateChangeRequestFromServiceNow: %v", err)
	}
	if resp.ChangeRequest.ID != crCreateSNID {
		t.Fatalf("CreateChangeRequestFromServiceNow id = %q, want %q", resp.ChangeRequest.ID, crCreateSNID)
	}
	assertChangeRequestAssignedTeam(t, scoped, repo, crCreateSNID, groupID)
}
