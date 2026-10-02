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

package repository_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wso2-open-operations/cs-tools/entity-service/internal/apierror"
	"github.com/wso2-open-operations/cs-tools/entity-service/internal/domain"
	"github.com/wso2-open-operations/cs-tools/entity-service/internal/repository"
)

// Skipped without CHANGE_REQUEST_TEST_DSN, the same convention every other
// repository integration test in this package uses.
//
//	CHANGE_REQUEST_TEST_DSN=postgres://... go test ./internal/repository/ -run ChangeRequestIntegration

const (
	changeRequestApprovalTestID          = "36666666-0000-0000-0000-000000000001"
	changeRequestApprovalApproverUserID  = "36666666-0000-0000-0000-000000000003"
	changeRequestApprovalApproverUserID2 = "36666666-0000-0000-0000-000000000004"
	changeRequestApprovalApproverUserID3 = "36666666-0000-0000-0000-000000000005"

	// changeRequestAssignedTeamTestID is its own id, distinct from
	// changeRequestApprovalTestID above, so the AssignedTeamID tests below
	// never race the approval tests' seed/cleanup of the same work_item row.
	changeRequestAssignedTeamTestID = "36666666-0000-0000-0000-000000000006"

	// seededGroupID is scripts/csm-compose/seed-entity-service.sql's one
	// "group" row ("Example Corp ABT", id 901) -- reused here rather than
	// inserting a fresh "group" row for this test alone, to avoid growing
	// that seed file for something it already covers.
	seededGroupID = "00000000-0000-0000-0000-000000000901"

	// unknownGroupID is a well-formed UUID that is not the id of any "group"
	// row -- used to exercise assignment_group_id's FK violation path.
	unknownGroupID = "36666666-aaaa-0000-0000-000000000000"

	// changeRequestAssessGateTestID is its own id, distinct from every other
	// test's change request above, so the Assess-gate/approver-provisioning
	// tests below never race any of them over the same work_item row.
	changeRequestAssessGateTestID = "36666666-0000-0000-0000-000000000007"

	// changeRequestAssessGateMemberUserID{,2} are seeded as team_member rows
	// against changeRequestAssessGateGroupID (distinct from every other
	// test's own user ids) to exercise Assess's auto-provisioned approvers.
	changeRequestAssessGateMemberUserID  = "36666666-0000-0000-0000-000000000008"
	changeRequestAssessGateMemberUserID2 = "36666666-0000-0000-0000-000000000009"

	// changeRequestAssessGateGroupID is its own dedicated "group" row,
	// deliberately NOT seededGroupID -- the membership-provisioning tests
	// below assert the *exact, exhaustive* set of a group's members, which
	// is unsafe to share with seededGroupID: that fixture is reused by other
	// tests (and by hand during local manual verification) for its mere FK
	// validity, with no guarantee nothing else ever attaches a team_member
	// row to it. A real collision of exactly this kind was hit once already
	// (a manual verification session left two real users' team_member rows
	// pointed at seededGroupID, inflating this test's own membership count
	// until that residue was cleaned up) -- a dedicated, test-owned group
	// makes that structurally impossible instead of merely unlikely.
	changeRequestAssessGateGroupID = "36666666-0000-0000-0000-00000000000a"
)

// seedApprovalUserForDecisionTest inserts one "user" row per given id --
// approval_stage_approver.approver_user_id's FK needs a real one -- as its
// own fresh rows (not relying on any of the local docker-compose stack's own
// incidental seed users) so this test only depends on migrations having run,
// the same assumption every other integration test in this package makes.
func seedApprovalUserForDecisionTest(t *testing.T, pool *pgxpool.Pool, userIDs ...string) {
	t.Helper()
	ctx := context.Background()
	if len(userIDs) == 0 {
		userIDs = []string{changeRequestApprovalApproverUserID}
	}

	for i, userID := range userIDs {
		id := userID
		cleanup := func() {
			_, _ = pool.Exec(ctx, `DELETE FROM "user" WHERE id = $1`, id)
		}
		cleanup()
		t.Cleanup(cleanup)

		email := fmt.Sprintf("cr-approval-test-%d@example.com", i+1)
		if _, err := pool.Exec(ctx,
			`INSERT INTO "user" (id, created_on, updated_on, created_by, updated_by, user_name, name, first_name, last_name, email, is_active, is_system_user)
			 VALUES ($1, now(), now(), 'cr-approval-test', 'cr-approval-test', $2, 'CR Approval Test', 'CR', 'Approval Test', $2, true, false)`,
			id, email); err != nil {
			t.Fatalf("seed approver user %s: %v", id, err)
		}
	}
}

// seedChangeRequestForApprovalTest inserts a minimal work_item/change_request
// pair in the given state -- enough for PatchChangeRequest's own read (via
// GetChangeRequestByID at the end of a successful patch) to resolve, since
// every other join in changeRequestFromJoins is a LEFT JOIN.
func seedChangeRequestForApprovalTest(t *testing.T, pool *repository.Scoped, state string) {
	t.Helper()
	// work_item and change_request are RLS-protected; seed/cleanup as internal.
	ctx := repository.WithSystemIdentity(context.Background())

	cleanup := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM work_item WHERE id = $1`, changeRequestApprovalTestID)
	}
	cleanup()
	t.Cleanup(cleanup)

	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed (%.60s): %v", sql, err)
		}
	}

	mustExec(`INSERT INTO work_item (id, created_on, updated_on, created_by, updated_by, number, subject, type)
	          VALUES ($1, now(), now(), 'cr-approval-test', 'cr-approval-test', 'CRAPPRV01', 'approval guard test', 'CHANGE_REQUEST')`,
		changeRequestApprovalTestID)
	mustExec(`INSERT INTO change_request (id, state) VALUES ($1, $2::change_request_state_enum)`,
		changeRequestApprovalTestID, state)
}

// TestChangeRequestIntegration_RequestApprovalIsBookkeepingOnly confirms the
// corrected behavior: {requestApproval: true} always sets
// change_request.approval = 'REQUESTED' and never touches state, regardless
// of the change request's current state. This replaces two prior tests
// (TestChangeRequestIntegration_RequestApprovalRejectsNonNewState/
// TestChangeRequestIntegration_RequestApprovalAdvancesNewToAssess) that
// asserted the earlier, now-confirmed-wrong model -- PatchChangeRequest used
// to also force state=ASSESS for a change request in New (and reject the
// request with a ConflictError for any other state) modeling New->Assess as
// an approval-gated ceremony. Checked against the real ServiceNow instance:
// New->Assess is a plain, ungated state change like every other transition,
// unrelated to approval at all -- the one real approval-gated transition is
// Assess->Authorize, handled entirely by DecideChangeRequestApproval, which
// this test does not touch.
func TestChangeRequestIntegration_RequestApprovalIsBookkeepingOnly(t *testing.T) {
	dsn := os.Getenv("CHANGE_REQUEST_TEST_DSN")
	if dsn == "" {
		t.Skip("CHANGE_REQUEST_TEST_DSN not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	scoped := repository.NewScoped(pool)
	sys := repository.WithSystemIdentity(context.Background())
	repo := repository.NewChangeRequestRepository(scoped)

	for _, seedState := range []string{"NEW", "REVIEW", "CLOSED"} {
		t.Run(seedState, func(t *testing.T) {
			seedChangeRequestForApprovalTest(t, scoped, seedState)

			yes := true
			_, err := repo.PatchChangeRequest(sys, changeRequestApprovalTestID,
				domain.PatchChangeRequestRequest{RequestApproval: &yes}, "cr-approval-test")
			if err != nil {
				t.Fatalf("PatchChangeRequest(requestApproval=true) on a %s-state change request: %v", seedState, err)
			}

			var gotState, gotApproval *string
			if scanErr := scoped.QueryRow(sys,
				`SELECT state::TEXT, approval::TEXT FROM change_request WHERE id = $1`, changeRequestApprovalTestID).
				Scan(&gotState, &gotApproval); scanErr != nil {
				t.Fatalf("read back state/approval: %v", scanErr)
			}
			if gotState == nil || *gotState != seedState {
				t.Fatalf("state after requestApproval=true = %v, want unchanged %q", gotState, seedState)
			}
			if gotApproval == nil || *gotApproval != "REQUESTED" {
				t.Fatalf("approval after requestApproval=true = %v, want \"REQUESTED\"", gotApproval)
			}
		})
	}
}

// seedApprovalStageForDecisionTest inserts one approval_stage plus one or
// more approval_stage_approver rows for changeRequestApprovalTestID -- the
// same shared seed changeRequestForApprovalTest/seedChangeRequestForApprovalTest
// use, extended with an actual approval stage so DecideChangeRequestApproval
// has something real to act on. Each entry in approverUserIDs gets its own
// requested approver row; the returned stage id lets a test seed additional
// rows (e.g. a second approver already rejected) directly.
func seedApprovalStageForDecisionTest(t *testing.T, pool *repository.Scoped, approverUserIDs ...string) string {
	t.Helper()
	// approval_stage and approval_stage_approver are RLS-protected; seed/cleanup as internal.
	ctx := repository.WithSystemIdentity(context.Background())

	stageID := "36666666-0000-0000-0000-000000000002"
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed (%.60s): %v", sql, err)
		}
	}
	mustExec(`INSERT INTO approval_stage (id, created_on, updated_on, created_by, updated_by, work_item_id, raw_status)
	          VALUES ($1, now(), now(), 'cr-approval-test', 'cr-approval-test', $2, 'requested')`,
		stageID, changeRequestApprovalTestID)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM approval_stage WHERE id = $1`, stageID)
	})

	for i, userID := range approverUserIDs {
		mustExec(`INSERT INTO approval_stage_approver (id, created_on, updated_on, created_by, updated_by, stage_id, work_item_id, approver_user_id, status)
		          VALUES ($1, now(), now(), 'cr-approval-test', 'cr-approval-test', $2, $3, $4, 'requested')`,
			fmt.Sprintf("36666666-0000-0000-0000-00000000%04d", i+10), stageID, changeRequestApprovalTestID, userID)
	}
	return stageID
}

// TestChangeRequestIntegration_DecideApprovalCascadesAssessToAuthorize is the
// regression guard for a real, reported gap: DecideChangeRequestApproval used
// to only flip the one approval_stage_approver row and never touch
// change_request.state at all -- confirmed live against a real approval on
// this data source. Approving the sole (or last-standing) approver on a
// change request's Assess-stage approval, while it's actually sitting in
// Assess, must now advance it to Authorize.
func TestChangeRequestIntegration_DecideApprovalCascadesAssessToAuthorize(t *testing.T) {
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
	repo := repository.NewChangeRequestRepository(scoped)
	seedApprovalUserForDecisionTest(t, pool)
	seedChangeRequestForApprovalTest(t, scoped, "ASSESS")
	seedApprovalStageForDecisionTest(t, scoped, changeRequestApprovalApproverUserID)

	if _, err := repo.DecideChangeRequestApproval(sys, changeRequestApprovalTestID,
		changeRequestApprovalApproverUserID, "approved", "cr-approval-test"); err != nil {
		t.Fatalf("DecideChangeRequestApproval(approved): %v", err)
	}

	var gotState string
	if scanErr := scoped.QueryRow(sys,
		`SELECT state::TEXT FROM change_request WHERE id = $1`, changeRequestApprovalTestID).Scan(&gotState); scanErr != nil {
		t.Fatalf("read back state: %v", scanErr)
	}
	if gotState != "AUTHORIZE" {
		t.Fatalf("state after approval = %q, want \"AUTHORIZE\"", gotState)
	}
}

// TestChangeRequestIntegration_DecideApprovalRejectionDoesNotCascade confirms
// a rejection never advances state, even though it resolves the stage (the
// same first-responder-wins quorum rule that lets a single approval resolve
// one also lets a single rejection resolve one) -- there is no confirmed
// target for a rejected Assess stage in this schema, so this must be a
// pure no-op on change_request.state.
func TestChangeRequestIntegration_DecideApprovalRejectionDoesNotCascade(t *testing.T) {
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
	repo := repository.NewChangeRequestRepository(scoped)
	seedApprovalUserForDecisionTest(t, pool)
	seedChangeRequestForApprovalTest(t, scoped, "ASSESS")
	seedApprovalStageForDecisionTest(t, scoped, changeRequestApprovalApproverUserID)

	if _, err := repo.DecideChangeRequestApproval(sys, changeRequestApprovalTestID,
		changeRequestApprovalApproverUserID, "rejected", "cr-approval-test"); err != nil {
		t.Fatalf("DecideChangeRequestApproval(rejected): %v", err)
	}

	var gotState string
	if scanErr := scoped.QueryRow(sys,
		`SELECT state::TEXT FROM change_request WHERE id = $1`, changeRequestApprovalTestID).Scan(&gotState); scanErr != nil {
		t.Fatalf("read back state: %v", scanErr)
	}
	if gotState != "ASSESS" {
		t.Fatalf("state after rejection = %q, want unchanged \"ASSESS\"", gotState)
	}
}

// TestChangeRequestIntegration_DecideApprovalDoesNotCascadeOutsideAssess
// confirms the cascade is scoped exactly to Assess->Authorize: approving an
// approver on a change request that isn't currently in Assess (e.g. one
// already sitting in Authorize, mid its own separate approval stage) must
// leave state untouched -- this repository deliberately does not attempt
// Authorize's own outgoing cascade yet.
func TestChangeRequestIntegration_DecideApprovalDoesNotCascadeOutsideAssess(t *testing.T) {
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
	repo := repository.NewChangeRequestRepository(scoped)
	seedApprovalUserForDecisionTest(t, pool)
	seedChangeRequestForApprovalTest(t, scoped, "AUTHORIZE")
	seedApprovalStageForDecisionTest(t, scoped, changeRequestApprovalApproverUserID)

	if _, err := repo.DecideChangeRequestApproval(sys, changeRequestApprovalTestID,
		changeRequestApprovalApproverUserID, "approved", "cr-approval-test"); err != nil {
		t.Fatalf("DecideChangeRequestApproval(approved): %v", err)
	}

	var gotState string
	if scanErr := scoped.QueryRow(sys,
		`SELECT state::TEXT FROM change_request WHERE id = $1`, changeRequestApprovalTestID).Scan(&gotState); scanErr != nil {
		t.Fatalf("read back state: %v", scanErr)
	}
	if gotState != "AUTHORIZE" {
		t.Fatalf("state after approval outside Assess = %q, want unchanged \"AUTHORIZE\"", gotState)
	}
}

// TestChangeRequestIntegration_DecideApprovalCancelsSiblingApprovers is the
// regression guard for the other real gap in the original cascade fix:
// resolving a stage used to leave every other still-pending approver on it
// sitting at Requested forever, with no visible way to tell "this group has
// already been decided" apart from "nobody has looked at this yet". Real
// ServiceNow does not do this (confirmed live against a genuine
// multi-approver group): once enough approvers respond to resolve the group,
// every other pending approver on it is moved to Cancelled. This test seeds
// three approvers on one stage and confirms that deciding as just one of
// them resolves the stage AND cancels the other two -- not just the acted-on
// row.
func TestChangeRequestIntegration_DecideApprovalCancelsSiblingApprovers(t *testing.T) {
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
	repo := repository.NewChangeRequestRepository(scoped)
	seedApprovalUserForDecisionTest(t, pool,
		changeRequestApprovalApproverUserID, changeRequestApprovalApproverUserID2, changeRequestApprovalApproverUserID3)
	seedChangeRequestForApprovalTest(t, scoped, "ASSESS")
	stageID := seedApprovalStageForDecisionTest(t, scoped,
		changeRequestApprovalApproverUserID, changeRequestApprovalApproverUserID2, changeRequestApprovalApproverUserID3)

	if _, err := repo.DecideChangeRequestApproval(sys, changeRequestApprovalTestID,
		changeRequestApprovalApproverUserID, "approved", "cr-approval-test"); err != nil {
		t.Fatalf("DecideChangeRequestApproval(approved): %v", err)
	}

	rows, err := scoped.Query(sys,
		`SELECT approver_user_id, status FROM approval_stage_approver WHERE stage_id = $1`, stageID)
	if err != nil {
		t.Fatalf("read back approver statuses: %v", err)
	}
	defer rows.Close()

	statusByApprover := map[string]string{}
	for rows.Next() {
		var approverID, status string
		if err := rows.Scan(&approverID, &status); err != nil {
			t.Fatalf("scan approver row: %v", err)
		}
		statusByApprover[approverID] = status
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate approver rows: %v", err)
	}

	if got := statusByApprover[changeRequestApprovalApproverUserID]; got != "approved" {
		t.Errorf("acted-on approver status = %q, want \"approved\"", got)
	}
	if got := statusByApprover[changeRequestApprovalApproverUserID2]; got != "cancelled" {
		t.Errorf("sibling approver 2 status = %q, want \"cancelled\" (not left at \"requested\")", got)
	}
	if got := statusByApprover[changeRequestApprovalApproverUserID3]; got != "cancelled" {
		t.Errorf("sibling approver 3 status = %q, want \"cancelled\" (not left at \"requested\")", got)
	}

	var gotState string
	if scanErr := scoped.QueryRow(sys,
		`SELECT state::TEXT FROM change_request WHERE id = $1`, changeRequestApprovalTestID).Scan(&gotState); scanErr != nil {
		t.Fatalf("read back change request state: %v", scanErr)
	}
	if gotState != "AUTHORIZE" {
		t.Fatalf("state after multi-approver approval = %q, want \"AUTHORIZE\"", gotState)
	}
}

// seedChangeRequestForAssignedTeamTest inserts a minimal work_item/
// change_request pair for the AssignedTeamID tests below, following the same
// shape as seedChangeRequestForApprovalTest but under its own id so the two
// test groups can never collide.
func seedChangeRequestForAssignedTeamTest(t *testing.T, pool *repository.Scoped) {
	t.Helper()
	// work_item and change_request are RLS-protected; seed/cleanup as internal.
	ctx := repository.WithSystemIdentity(context.Background())

	cleanup := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM work_item WHERE id = $1`, changeRequestAssignedTeamTestID)
	}
	cleanup()
	t.Cleanup(cleanup)

	if _, err := pool.Exec(ctx,
		`INSERT INTO work_item (id, created_on, updated_on, created_by, updated_by, number, subject, type)
		 VALUES ($1, now(), now(), 'cr-assigned-team-test', 'cr-assigned-team-test', 'CRTEAM001', 'assigned team patch test', 'CHANGE_REQUEST')`,
		changeRequestAssignedTeamTestID); err != nil {
		t.Fatalf("seed work_item: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO change_request (id, state) VALUES ($1, 'NEW'::change_request_state_enum)`,
		changeRequestAssignedTeamTestID); err != nil {
		t.Fatalf("seed change_request: %v", err)
	}
}

// TestChangeRequestIntegration_PatchAssignedTeamID is the regression guard for
// the bug this change fixes: PatchChangeRequestRequest.AssignedTeamID used to
// be read but never written anywhere in PatchChangeRequest's own UPDATE,
// unlike the adjacent AssignedEngineerID handling. Patching it to a real
// "group" id must round-trip through work_item.assignment_group_id and come
// back as ChangeRequest.AssignedTeam on a subsequent read.
func TestChangeRequestIntegration_PatchAssignedTeamID(t *testing.T) {
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
	repo := repository.NewChangeRequestRepository(scoped)
	seedChangeRequestForAssignedTeamTest(t, scoped)
	sys := repository.WithSystemIdentity(context.Background())

	teamID := seededGroupID
	updated, err := repo.PatchChangeRequest(sys, changeRequestAssignedTeamTestID,
		domain.PatchChangeRequestRequest{AssignedTeamID: &teamID}, "cr-assigned-team-test")
	if err != nil {
		t.Fatalf("PatchChangeRequest(assignedTeamId=%s): %v", teamID, err)
	}
	if updated.AssignedTeam == nil || updated.AssignedTeam.ID != teamID {
		t.Fatalf("PatchChangeRequest response AssignedTeam = %+v, want ID %q", updated.AssignedTeam, teamID)
	}

	// Read back directly, independent of the repository's own response, to
	// confirm the column itself -- not just the in-memory return value --
	// actually changed.
	var gotAssignmentGroupID string
	if scanErr := scoped.QueryRow(sys,
		`SELECT assignment_group_id::TEXT FROM work_item WHERE id = $1`, changeRequestAssignedTeamTestID).
		Scan(&gotAssignmentGroupID); scanErr != nil {
		t.Fatalf("read back assignment_group_id: %v", scanErr)
	}
	if gotAssignmentGroupID != teamID {
		t.Fatalf("work_item.assignment_group_id = %q, want %q", gotAssignmentGroupID, teamID)
	}

	// GetChangeRequestByID, the way a caller would actually re-read the
	// change request, must agree too.
	fetched, err := repo.GetChangeRequestByID(sys, changeRequestAssignedTeamTestID)
	if err != nil {
		t.Fatalf("GetChangeRequestByID: %v", err)
	}
	if fetched.AssignedTeam == nil || fetched.AssignedTeam.ID != teamID {
		t.Fatalf("GetChangeRequestByID AssignedTeam = %+v, want ID %q", fetched.AssignedTeam, teamID)
	}
}

// TestChangeRequestIntegration_PatchAssignedTeamIDUnknownTeamIsValidationError
// confirms an unknown team id produces a clean ValidationError (the
// changeRequestPatchFKField mapping's "assignedTeamId" entry), not a raw
// Postgres foreign-key-violation error surfaced to the caller.
func TestChangeRequestIntegration_PatchAssignedTeamIDUnknownTeamIsValidationError(t *testing.T) {
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
	repo := repository.NewChangeRequestRepository(scoped)
	seedChangeRequestForAssignedTeamTest(t, scoped)
	sys := repository.WithSystemIdentity(context.Background())

	badTeamID := unknownGroupID
	_, err = repo.PatchChangeRequest(sys, changeRequestAssignedTeamTestID,
		domain.PatchChangeRequestRequest{AssignedTeamID: &badTeamID}, "cr-assigned-team-test")
	if err == nil {
		t.Fatal("PatchChangeRequest(assignedTeamId=<unknown>) succeeded, want a ValidationError")
	}
	var valErr *apierror.ValidationError
	if !errors.As(err, &valErr) {
		t.Fatalf("PatchChangeRequest(assignedTeamId=<unknown>) error = %v (%T), want *apierror.ValidationError", err, err)
	}
	if valErr.Msg == "" {
		t.Fatal("ValidationError.Msg is empty")
	}

	// The column must be left untouched by the rolled-back transaction.
	var gotAssignmentGroupID *string
	if scanErr := scoped.QueryRow(sys,
		`SELECT assignment_group_id::TEXT FROM work_item WHERE id = $1`, changeRequestAssignedTeamTestID).
		Scan(&gotAssignmentGroupID); scanErr != nil {
		t.Fatalf("read back assignment_group_id: %v", scanErr)
	}
	if gotAssignmentGroupID != nil {
		t.Fatalf("work_item.assignment_group_id = %v after a failed patch, want unchanged NULL", *gotAssignmentGroupID)
	}
}

// seedChangeRequestForAssessGateTest inserts a minimal NEW-state work_item/
// change_request pair with no assigned team -- the starting point for every
// test below.
func seedChangeRequestForAssessGateTest(t *testing.T, pool *repository.Scoped) {
	t.Helper()
	// work_item and change_request are RLS-protected; seed/cleanup as internal.
	ctx := repository.WithSystemIdentity(context.Background())

	cleanup := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM work_item WHERE id = $1`, changeRequestAssessGateTestID)
	}
	cleanup()
	t.Cleanup(cleanup)

	if _, err := pool.Exec(ctx,
		`INSERT INTO work_item (id, created_on, updated_on, created_by, updated_by, number, subject, type)
		 VALUES ($1, now(), now(), 'cr-assess-gate-test', 'cr-assess-gate-test', 'CRASSESS01', 'assess gate test', 'CHANGE_REQUEST')`,
		changeRequestAssessGateTestID); err != nil {
		t.Fatalf("seed work_item: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO change_request (id, state) VALUES ($1, 'NEW'::change_request_state_enum)`,
		changeRequestAssessGateTestID); err != nil {
		t.Fatalf("seed change_request: %v", err)
	}
}

// seedAssessGateGroup inserts changeRequestAssessGateGroupID's own "group"
// row -- a dedicated fixture for the tests below, deliberately not
// seededGroupID (see that constant's own doc comment on the collision this
// avoids).
func seedAssessGateGroup(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()

	cleanup := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM "group" WHERE id = $1`, changeRequestAssessGateGroupID)
	}
	cleanup()
	t.Cleanup(cleanup)

	if _, err := pool.Exec(ctx,
		`INSERT INTO "group" (id, created_on, updated_on, created_by, updated_by, name)
		 VALUES ($1, now(), now(), 'cr-assess-gate-test', 'cr-assess-gate-test', 'Assess Gate Test Group')`,
		changeRequestAssessGateGroupID); err != nil {
		t.Fatalf("seed group: %v", err)
	}
}

// seedTeamMembersForAssessGateTest inserts one "user" row and one
// team_member row (keyed by group_id, not team_id -- see
// PatchChangeRequest's own doc comment on why) per given user id, so they
// resolve as members of changeRequestAssessGateGroupID for the
// auto-provisioning tests below. Callers must seed that group row first
// (seedAssessGateGroup) -- group_id's FK requires it to already exist.
func seedTeamMembersForAssessGateTest(t *testing.T, pool *pgxpool.Pool, userIDs ...string) {
	t.Helper()
	ctx := context.Background()

	for i, userID := range userIDs {
		id := userID
		userCleanup := func() {
			_, _ = pool.Exec(ctx, `DELETE FROM "user" WHERE id = $1`, id)
		}
		userCleanup()
		t.Cleanup(userCleanup)

		email := fmt.Sprintf("cr-assess-gate-member-%d@example.com", i+1)
		if _, err := pool.Exec(ctx,
			`INSERT INTO "user" (id, created_on, updated_on, created_by, updated_by, user_name, name, first_name, last_name, email, is_active, is_system_user)
			 VALUES ($1, now(), now(), 'cr-assess-gate-test', 'cr-assess-gate-test', $2, 'Assess Gate Member', 'Assess', 'Gate Member', $2, true, false)`,
			id, email); err != nil {
			t.Fatalf("seed team member user %s: %v", id, err)
		}

		memberCleanup := func() {
			_, _ = pool.Exec(ctx, `DELETE FROM team_member WHERE user_id = $1`, id)
		}
		memberCleanup()
		t.Cleanup(memberCleanup)

		// team_member.team_id is NOT NULL, but no test here exercises the
		// internal team registry -- the one seeded team row
		// (scripts/csm-compose/seed-entity-service.sql's "Example Corp ABT",
		// id 901) satisfies the column's NOT NULL constraint without
		// implying anything about this test's own group_id-keyed membership
		// (changeRequestAssessGateGroupID, a wholly separate "group" row).
		if _, err := pool.Exec(ctx,
			`INSERT INTO team_member (id, created_on, updated_on, created_by, updated_by, team_id, user_id, group_id)
			 VALUES (gen_random_uuid(), now(), now(), 'cr-assess-gate-test', 'cr-assess-gate-test', $1::uuid, $2, $3::uuid)`,
			seededGroupID, id, changeRequestAssessGateGroupID); err != nil {
			t.Fatalf("seed team_member for user %s: %v", id, err)
		}
	}
}

// TestChangeRequestIntegration_PatchAssessRequiresAssignedTeam is the
// regression guard for the compulsory gate: PatchChangeRequest must reject a
// {state: "assess"} patch with a clean ValidationError when the change
// request has no assigned team and the request itself doesn't supply one --
// never a silent state change with nothing to assign the Assess stage to.
func TestChangeRequestIntegration_PatchAssessRequiresAssignedTeam(t *testing.T) {
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
	repo := repository.NewChangeRequestRepository(scoped)
	seedChangeRequestForAssessGateTest(t, scoped)

	assess := domain.ChangeRequestStateAssess
	_, err = repo.PatchChangeRequest(sys, changeRequestAssessGateTestID,
		domain.PatchChangeRequestRequest{State: &assess}, "cr-assess-gate-test")
	if err == nil {
		t.Fatal("PatchChangeRequest(state=assess) with no assigned team succeeded, want a ValidationError")
	}
	var valErr *apierror.ValidationError
	if !errors.As(err, &valErr) {
		t.Fatalf("PatchChangeRequest(state=assess) with no assigned team error = %v (%T), want *apierror.ValidationError", err, err)
	}

	var gotState string
	if scanErr := scoped.QueryRow(sys,
		`SELECT state::TEXT FROM change_request WHERE id = $1`, changeRequestAssessGateTestID).Scan(&gotState); scanErr != nil {
		t.Fatalf("read back state: %v", scanErr)
	}
	if gotState != "NEW" {
		t.Fatalf("state after a rejected assess patch = %q, want unchanged \"NEW\"", gotState)
	}
}

// TestChangeRequestIntegration_PatchAssessWithTeamAlreadyOnRecordSucceeds
// confirms the compulsory gate accepts a team set by an earlier, separate
// PATCH (the real flow: the Edit dialog saves assignedTeamId first, then
// "Move to Assess" is sent as its own request with no assignedTeamId at
// all) -- not just a team supplied in the very same request.
func TestChangeRequestIntegration_PatchAssessWithTeamAlreadyOnRecordSucceeds(t *testing.T) {
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
	repo := repository.NewChangeRequestRepository(scoped)
	seedChangeRequestForAssessGateTest(t, scoped)
	seedAssessGateGroup(t, pool)
	// A non-empty group: the Assess provisioning path now rejects an empty
	// one outright (see TestChangeRequestIntegration_PatchAssessRejectsEmptyGroup),
	// so this test -- about the team-already-on-record path specifically --
	// needs a real member for its own second PatchChangeRequest call to
	// reach "succeeds" at all.
	seedTeamMembersForAssessGateTest(t, pool, changeRequestAssessGateMemberUserID)
	t.Cleanup(func() {
		_, _ = scoped.Exec(sys, `DELETE FROM approval_stage WHERE work_item_id = $1`, changeRequestAssessGateTestID)
	})

	teamID := changeRequestAssessGateGroupID
	if _, err := repo.PatchChangeRequest(sys, changeRequestAssessGateTestID,
		domain.PatchChangeRequestRequest{AssignedTeamID: &teamID}, "cr-assess-gate-test"); err != nil {
		t.Fatalf("PatchChangeRequest(assignedTeamId=%s): %v", teamID, err)
	}

	assess := domain.ChangeRequestStateAssess
	updated, err := repo.PatchChangeRequest(sys, changeRequestAssessGateTestID,
		domain.PatchChangeRequestRequest{State: &assess}, "cr-assess-gate-test")
	if err != nil {
		t.Fatalf("PatchChangeRequest(state=assess) with a team already on the record: %v", err)
	}
	if updated.State == nil || *updated.State != string(assess) {
		t.Fatalf("state after patch = %v, want %q", updated.State, assess)
	}
}

// TestChangeRequestIntegration_PatchAssessProvisionsApproversFromGroupMembers
// is the regression guard for the auto-provisioning feature: the moment a
// change request enters Assess, every team_member row keyed to the assigned
// team's group_id must become a Requested approval_stage_approver on a
// freshly created Assess-stage approval_stage -- not an empty Approvals tab
// with nobody to approve it.
func TestChangeRequestIntegration_PatchAssessProvisionsApproversFromGroupMembers(t *testing.T) {
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
	repo := repository.NewChangeRequestRepository(scoped)
	seedChangeRequestForAssessGateTest(t, scoped)
	seedAssessGateGroup(t, pool)
	seedTeamMembersForAssessGateTest(t, pool, changeRequestAssessGateMemberUserID, changeRequestAssessGateMemberUserID2)
	t.Cleanup(func() {
		_, _ = scoped.Exec(sys, `DELETE FROM approval_stage WHERE work_item_id = $1`, changeRequestAssessGateTestID)
	})

	teamID := changeRequestAssessGateGroupID
	assess := domain.ChangeRequestStateAssess
	if _, err := repo.PatchChangeRequest(sys, changeRequestAssessGateTestID,
		domain.PatchChangeRequestRequest{AssignedTeamID: &teamID, State: &assess}, "cr-assess-gate-test"); err != nil {
		t.Fatalf("PatchChangeRequest(assignedTeamId=%s, state=assess): %v", teamID, err)
	}

	var stageID, stageGroupID string
	if scanErr := scoped.QueryRow(sys,
		`SELECT id, assignment_group_id::TEXT FROM approval_stage WHERE work_item_id = $1`, changeRequestAssessGateTestID).
		Scan(&stageID, &stageGroupID); scanErr != nil {
		t.Fatalf("read back approval_stage: %v", scanErr)
	}
	if stageGroupID != teamID {
		t.Fatalf("approval_stage.assignment_group_id = %q, want %q", stageGroupID, teamID)
	}

	rows, err := scoped.Query(sys,
		`SELECT approver_user_id::TEXT, status FROM approval_stage_approver WHERE stage_id = $1 ORDER BY approver_user_id`, stageID)
	if err != nil {
		t.Fatalf("query approval_stage_approver: %v", err)
	}
	defer rows.Close()
	gotApprovers := map[string]string{}
	for rows.Next() {
		var uid, status string
		if err := rows.Scan(&uid, &status); err != nil {
			t.Fatalf("scan approval_stage_approver: %v", err)
		}
		gotApprovers[uid] = status
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("approval_stage_approver rows: %v", err)
	}

	want := map[string]string{
		changeRequestAssessGateMemberUserID:  "requested",
		changeRequestAssessGateMemberUserID2: "requested",
	}
	if len(gotApprovers) != len(want) {
		t.Fatalf("approval_stage_approver rows = %+v, want exactly %+v", gotApprovers, want)
	}
	for uid, wantStatus := range want {
		if gotApprovers[uid] != wantStatus {
			t.Fatalf("approver %s status = %q, want %q", uid, gotApprovers[uid], wantStatus)
		}
	}
}

// TestChangeRequestIntegration_PatchAssessDoesNotReprovisionWhenStageExists
// confirms a second {state: "assess"} patch against a change request that
// already has an approval_stage (e.g. a resend, or an unrelated field edit
// sent while already in Assess) never creates a duplicate stage or
// re-seeds approvers -- DecideChangeRequestApproval owns everything about
// an existing stage from the moment it's created.
func TestChangeRequestIntegration_PatchAssessDoesNotReprovisionWhenStageExists(t *testing.T) {
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
	repo := repository.NewChangeRequestRepository(scoped)
	seedChangeRequestForAssessGateTest(t, scoped)
	seedAssessGateGroup(t, pool)
	seedTeamMembersForAssessGateTest(t, pool, changeRequestAssessGateMemberUserID, changeRequestAssessGateMemberUserID2)
	t.Cleanup(func() {
		_, _ = scoped.Exec(sys, `DELETE FROM approval_stage WHERE work_item_id = $1`, changeRequestAssessGateTestID)
	})

	teamID := changeRequestAssessGateGroupID
	assess := domain.ChangeRequestStateAssess
	if _, err := repo.PatchChangeRequest(sys, changeRequestAssessGateTestID,
		domain.PatchChangeRequestRequest{AssignedTeamID: &teamID, State: &assess}, "cr-assess-gate-test"); err != nil {
		t.Fatalf("PatchChangeRequest(assignedTeamId=%s, state=assess) #1: %v", teamID, err)
	}
	if _, err := repo.PatchChangeRequest(sys, changeRequestAssessGateTestID,
		domain.PatchChangeRequestRequest{State: &assess}, "cr-assess-gate-test"); err != nil {
		t.Fatalf("PatchChangeRequest(state=assess) #2 (resend): %v", err)
	}

	var stageCount int
	if scanErr := scoped.QueryRow(sys,
		`SELECT COUNT(*) FROM approval_stage WHERE work_item_id = $1`, changeRequestAssessGateTestID).Scan(&stageCount); scanErr != nil {
		t.Fatalf("count approval_stage: %v", scanErr)
	}
	if stageCount != 1 {
		t.Fatalf("approval_stage rows after two assess patches = %d, want exactly 1", stageCount)
	}

	var approverCount int
	if scanErr := scoped.QueryRow(sys,
		`SELECT COUNT(*) FROM approval_stage_approver asa JOIN approval_stage ast ON ast.id = asa.stage_id WHERE ast.work_item_id = $1`,
		changeRequestAssessGateTestID).Scan(&approverCount); scanErr != nil {
		t.Fatalf("count approval_stage_approver: %v", scanErr)
	}
	if approverCount != 2 {
		t.Fatalf("approval_stage_approver rows after two assess patches = %d, want exactly 2 (not re-seeded)", approverCount)
	}
}

// TestChangeRequestIntegration_PatchAssessRejectsEmptyGroup is the
// regression guard for a CodeRabbit-caught gap: the approval_stage used to
// be created before team_member was ever queried, so an assigned team with
// no members still committed an empty, un-approvable stage -- the change
// request would be stuck in Assess forever, since "no approval_stage exists
// yet" is exactly the condition that gates (re-)provisioning. A team with no
// members must instead reject the whole PATCH with a ValidationError and
// leave no approval_stage behind at all.
func TestChangeRequestIntegration_PatchAssessRejectsEmptyGroup(t *testing.T) {
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
	repo := repository.NewChangeRequestRepository(scoped)
	seedChangeRequestForAssessGateTest(t, scoped)
	seedAssessGateGroup(t, pool)
	// Deliberately no seedTeamMembersForAssessGateTest call -- the group
	// exists (so assignedTeamId itself is valid) but has zero members.
	t.Cleanup(func() {
		_, _ = scoped.Exec(sys, `DELETE FROM approval_stage WHERE work_item_id = $1`, changeRequestAssessGateTestID)
	})

	teamID := changeRequestAssessGateGroupID
	assess := domain.ChangeRequestStateAssess
	_, err = repo.PatchChangeRequest(sys, changeRequestAssessGateTestID,
		domain.PatchChangeRequestRequest{AssignedTeamID: &teamID, State: &assess}, "cr-assess-gate-test")
	if err == nil {
		t.Fatal("PatchChangeRequest(state=assess) with an empty assigned team succeeded, want a ValidationError")
	}
	var valErr *apierror.ValidationError
	if !errors.As(err, &valErr) {
		t.Fatalf("PatchChangeRequest(state=assess) with an empty assigned team error = %v (%T), want *apierror.ValidationError", err, err)
	}

	var gotState string
	if scanErr := scoped.QueryRow(sys,
		`SELECT state::TEXT FROM change_request WHERE id = $1`, changeRequestAssessGateTestID).Scan(&gotState); scanErr != nil {
		t.Fatalf("read back state: %v", scanErr)
	}
	if gotState != "NEW" {
		t.Fatalf("state after a rejected assess patch = %q, want unchanged \"NEW\"", gotState)
	}

	var stageCount int
	if scanErr := scoped.QueryRow(sys,
		`SELECT COUNT(*) FROM approval_stage WHERE work_item_id = $1`, changeRequestAssessGateTestID).Scan(&stageCount); scanErr != nil {
		t.Fatalf("count approval_stage: %v", scanErr)
	}
	if stageCount != 0 {
		t.Fatalf("approval_stage rows after a rejected assess patch = %d, want 0 (no empty stage left behind)", stageCount)
	}
}

// TestChangeRequestIntegration_PatchAssessDeduplicatesGroupMembers is the
// regression guard for a second CodeRabbit catch: team_member has no unique
// constraint on (user_id, group_id), so a duplicated membership row must
// still provision exactly one requested approval_stage_approver per person,
// never two.
func TestChangeRequestIntegration_PatchAssessDeduplicatesGroupMembers(t *testing.T) {
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
	repo := repository.NewChangeRequestRepository(scoped)
	seedChangeRequestForAssessGateTest(t, scoped)
	seedAssessGateGroup(t, pool)
	seedTeamMembersForAssessGateTest(t, pool, changeRequestAssessGateMemberUserID)
	t.Cleanup(func() {
		_, _ = scoped.Exec(sys, `DELETE FROM approval_stage WHERE work_item_id = $1`, changeRequestAssessGateTestID)
	})

	// A second team_member row for the SAME user against the SAME group --
	// seedTeamMembersForAssessGateTest's own cleanup (DELETE ... WHERE
	// user_id = $1) already covers this row too, since it shares the user id.
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO team_member (id, created_on, updated_on, created_by, updated_by, team_id, user_id, group_id)
		 VALUES (gen_random_uuid(), now(), now(), 'cr-assess-gate-test', 'cr-assess-gate-test', $1::uuid, $2, $3::uuid)`,
		seededGroupID, changeRequestAssessGateMemberUserID, changeRequestAssessGateGroupID); err != nil {
		t.Fatalf("seed duplicate team_member row: %v", err)
	}

	teamID := changeRequestAssessGateGroupID
	assess := domain.ChangeRequestStateAssess
	if _, err := repo.PatchChangeRequest(sys, changeRequestAssessGateTestID,
		domain.PatchChangeRequestRequest{AssignedTeamID: &teamID, State: &assess}, "cr-assess-gate-test"); err != nil {
		t.Fatalf("PatchChangeRequest(assignedTeamId=%s, state=assess): %v", teamID, err)
	}

	var approverCount int
	if scanErr := scoped.QueryRow(sys,
		`SELECT COUNT(*) FROM approval_stage_approver asa JOIN approval_stage ast ON ast.id = asa.stage_id WHERE ast.work_item_id = $1`,
		changeRequestAssessGateTestID).Scan(&approverCount); scanErr != nil {
		t.Fatalf("count approval_stage_approver: %v", scanErr)
	}
	if approverCount != 1 {
		t.Fatalf("approval_stage_approver rows for a user with a duplicated team_member row = %d, want exactly 1", approverCount)
	}
}
