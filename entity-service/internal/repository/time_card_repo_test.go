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

// This is an integration test: it exercises the real SQL in
// time_card_repo.go's timeCardSelectColumns/scanTimeCardView against a live
// PostgreSQL instance, because the bug it regression-tests (a NULL
// analyzing_minutes/setting_up_minutes/reproducing_debugging_minutes/
// providing_solution_minutes/patching_minutes column crashing the scan with
// "cannot scan NULL into *int") can only be reproduced by a real NULL value
// coming back from a real query -- a fake TimeCardRepository can't
// reproduce a pgx scan error at all. Same DSN and skip-when-unset pattern as
// project_stats_repo_integration_test.go/project_case_stats_repo_integration_test.go
// (package repository_test, same package, so caseStatsPool below is
// reused):
//
//	CASE_STATS_TEST_DSN=postgres://... go test ./internal/repository/ -run TimeCardIntegration

package repository_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wso2-open-operations/cs-tools/entity-service/internal/domain"
	"github.com/wso2-open-operations/cs-tools/entity-service/internal/repository"
)

const (
	timeCardTestProjectID = "41111111-1111-1111-1111-111111111111"
	timeCardTestUserID    = "42222222-0000-0000-0000-000000000001"
	timeCardTestCaseID    = "45555555-0000-0000-0000-000000000001"
	// timeCardTestNullCardID deliberately has every one of the five
	// duration columns left NULL (omitted from the INSERT below) -- the
	// exact production shape that used to crash the scan.
	timeCardTestNullCardID = "46666666-0000-0000-0000-000000000001"
)

// seedTimeCardWithNullDurations creates one project/user/case and a single
// time_card row that leaves analyzing_minutes/setting_up_minutes/
// reproducing_debugging_minutes/providing_solution_minutes/patching_minutes
// all NULL -- every other repository integration test in this package seeds
// at least one of these columns (e.g. project_stats_repo_integration_test.go's
// analyzing_minutes: 60), which is exactly why this particular NULL
// combination went unexercised until this bug was reported.
func seedTimeCardWithNullDurations(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	// WithSystemIdentity: time_card's RLS policies (migration 0144)
	// require an identity on every statement now, including this seed's own
	// writes. scoped, not just pool, backs mustExec below.
	ctx := repository.WithSystemIdentity(context.Background())
	scoped := repository.NewScoped(pool)

	cleanup := func() {
		_, _ = scoped.Exec(ctx, `DELETE FROM time_card WHERE created_by = 'time-card-null-test'`)
		_, _ = scoped.Exec(ctx, `DELETE FROM work_item WHERE created_by = 'time-card-null-test'`)
		_, _ = pool.Exec(ctx, `DELETE FROM "user" WHERE user_name = 'time-card-null-test@example.com'`)
		_, _ = pool.Exec(ctx, `DELETE FROM project WHERE id = $1`, timeCardTestProjectID)
	}
	cleanup()
	t.Cleanup(cleanup)

	// Local closure, not a shared package-level helper -- same style as
	// project_stats_repo_integration_test.go's own seed function (this
	// package, repository_test, has no package-level mustExec of its own;
	// that name is otherwise only a same-named local closure per file, or
	// an unrelated package-level helper in the internal "repository" test
	// package, a different package from this one).
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := scoped.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed (%.60s): %v", sql, err)
		}
	}

	mustExec(`INSERT INTO project (id, created_on, updated_on, created_by, updated_by, key, sf_id, end_date)
	          VALUES ($1, now(), now(), 'time-card-null-test', 'time-card-null-test', 'TCNULLTEST', 'sf-tcnulltest', (now() + INTERVAL '30 days')::date)`,
		timeCardTestProjectID)

	mustExec(`INSERT INTO "user" (id, created_on, updated_on, user_name, email, is_active)
	          VALUES ($1, now(), now(), 'time-card-null-test@example.com', 'time-card-null-test@example.com', true)`,
		timeCardTestUserID)

	mustExec(`INSERT INTO work_item (id, created_on, updated_on, created_by, updated_by, number, wso2_id, subject, type, project_id)
	          VALUES ($1, now(), now(), 'time-card-null-test', 'time-card-null-test', 'TCNULL01', 'TCNULL-1', 'a case', 'CASE', $2)`,
		timeCardTestCaseID, timeCardTestProjectID)
	mustExec(`INSERT INTO "case" (id, state, severity) VALUES ($1, 'OPEN', 'S2')`, timeCardTestCaseID)

	// The five duration columns are deliberately omitted -- they stay NULL,
	// reproducing the exact row shape that crashed scanTimeCardView.
	mustExec(`INSERT INTO time_card (id, created_on, updated_on, created_by, updated_by, case_id, user_id, work_date, is_billable, state)
	          VALUES ($1, now(), now(), 'time-card-null-test', 'time-card-null-test', $2, $3, CURRENT_DATE, true, 'SUBMITTED')`,
		timeCardTestNullCardID, timeCardTestCaseID, timeCardTestUserID)
}

// TestTimeCardIntegration_SearchTimeCardsToleratesNullDurationColumns is the
// regression test for the /time-cards 500: previously, scanTimeCardView
// scanned analyzing_minutes/setting_up_minutes/reproducing_debugging_minutes/
// providing_solution_minutes/patching_minutes into plain (non-pointer) int
// locals, so a row with any of those five columns NULL failed with "cannot
// scan NULL into *int". timeCardSelectColumns now wraps every one of them in
// COALESCE(...,0) (matching project_stats_repo.go's own timeCardMinutesExpr
// precedent), so the scan must succeed and report zero for each.
func TestTimeCardIntegration_SearchTimeCardsToleratesNullDurationColumns(t *testing.T) {
	pool := caseStatsPool(t)
	seedTimeCardWithNullDurations(t, pool)

	repo := repository.NewTimeCardRepository(repository.NewScoped(pool))
	// WithSystemIdentity: this test is about NULL-duration scanning, not
	// authorization -- it never seeds a project_contact for
	// timeCardTestProjectID, so an internal/unrestricted identity is what
	// makes the row visible under time_card's RLS policy (migration 0144).
	ctx := repository.WithSystemIdentity(context.Background())
	views, total, err := repo.SearchTimeCards(ctx, domain.SearchTimeCardsRequest{
		Filters:    &domain.SearchTimeCardsFilters{CaseID: ptrTo(timeCardTestCaseID)},
		Pagination: domain.Pagination{Limit: 10, Offset: 0},
	}, "")
	if err != nil {
		t.Fatalf("SearchTimeCards() error = %v, want no error scanning a row with NULL duration columns", err)
	}
	if total != 1 || len(views) != 1 {
		t.Fatalf("SearchTimeCards() returned %d/%d rows, want exactly 1", len(views), total)
	}

	v := views[0]
	if v.TimeAnalyzing != 0 || v.TimeSettingUp != 0 || v.TimeReproducingDebugging != 0 ||
		v.TimeProvidingSolution != 0 || v.TimePatching != 0 {
		t.Errorf("duration fields = %+v, want all zero for a row whose underlying columns are NULL", v)
	}
}

func ptrTo(s string) *string { return &s }
