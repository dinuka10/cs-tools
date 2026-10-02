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

// Regression test for migration 0151: engagement/service_request/
// security_report_analysis (the three case-like work_item extension tables
// migration 0147 left unprotected) now carry their own RLS, exercised here
// through the real CaseRepository.GetCaseByID Go code -- not just the SQL
// policy in isolation -- via case_repo.go's caseLikeJoins, which is how
// these three tables are actually read in production. Runs against a real
// Postgres with 0151 applied. Skipped without CASE_STATS_TEST_DSN.
//
//	CASE_STATS_TEST_DSN=postgres://... go test ./internal/repository/ -run CaseLikeExtensionRLS

package repository_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wso2-open-operations/cs-tools/entity-service/internal/apierror"
	"github.com/wso2-open-operations/cs-tools/entity-service/internal/repository"
)

const (
	cleProjectID  = "70000000-0000-0000-0000-000000000001"
	cleAccountID  = "71111111-0000-0000-0000-000000000001"
	cleContactID  = "72222222-0000-0000-0000-000000000001"
	cleEngagement = "73333333-0000-0000-0000-000000000001"
	cleServiceReq = "73333333-0000-0000-0000-000000000002"
	cleSecReport  = "73333333-0000-0000-0000-000000000003"
	cleMember     = "cle-member@test.local"
	cleStranger   = "cle-stranger@test.local"
)

// seedCaseLikeExtensionFixture creates one project with one REGISTERED
// contact, and one work_item of each of the three unprotected-until-0151
// extension types.
func seedCaseLikeExtensionFixture(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := repository.WithSystemIdentity(context.Background())
	scoped := repository.NewScoped(pool)

	cleanup := func() {
		_, _ = scoped.Exec(ctx, `DELETE FROM engagement WHERE id = $1`, cleEngagement)
		_, _ = scoped.Exec(ctx, `DELETE FROM service_request WHERE id = $1`, cleServiceReq)
		_, _ = scoped.Exec(ctx, `DELETE FROM security_report_analysis WHERE id = $1`, cleSecReport)
		_, _ = scoped.Exec(ctx, `DELETE FROM work_item WHERE project_id = $1`, cleProjectID)
		_, _ = pool.Exec(ctx, `DELETE FROM project_contact WHERE project_id = $1`, cleProjectID)
		_, _ = pool.Exec(ctx, `DELETE FROM project WHERE id = $1`, cleProjectID)
		_, _ = pool.Exec(ctx, `DELETE FROM account_contact WHERE id = $1`, cleContactID)
		_, _ = pool.Exec(ctx, `DELETE FROM account WHERE id = $1`, cleAccountID)
	}
	cleanup()
	t.Cleanup(cleanup)

	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed (%.80s): %v", sql, err)
		}
	}
	mustExecScoped := func(sql string, args ...any) {
		t.Helper()
		if _, err := scoped.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed scoped (%.80s): %v", sql, err)
		}
	}
	now := time.Now().UTC()

	mustExec(`INSERT INTO account (id, created_on, updated_on, created_by, updated_by, name, number, sf_id)
		VALUES ($1, $2, $2, 'test', 'test', 'CLE Test Account', 'CLE-ACC-1', 'sf-cle-acc-1')`, cleAccountID, now)
	mustExec(`INSERT INTO account_contact (id, created_on, updated_on, created_by, updated_by, user_name, account_id)
		VALUES ($1, $2, $2, 'test', 'test', 'CLE Test Contact', $3)`, cleContactID, now, cleAccountID)
	mustExec(`INSERT INTO project (id, created_on, updated_on, created_by, updated_by, key, sf_id, account_id)
		VALUES ($1, $2, $2, 'test', 'test', 'CLETEST', 'sf-cletest', $3)`, cleProjectID, now, cleAccountID)
	mustExec(`INSERT INTO project_contact (id, created_on, updated_on, created_by, updated_by, email, account_contact_id, project_id, state)
		VALUES (gen_random_uuid(), $1, $1, 'test', 'test', $2, $3, $4, 'REGISTERED')`,
		now, cleMember, cleContactID, cleProjectID)

	mustExecScoped(`INSERT INTO work_item (id, created_on, updated_on, created_by, updated_by, number, wso2_id, subject, type, project_id)
		VALUES ($1, $2, $2, 'test', 'test', 'CLE-ENG-1', 'CLE-WSO2-ENG-1', 'cle engagement', 'ENGAGEMENT', $3)`,
		cleEngagement, now, cleProjectID)
	mustExecScoped(`INSERT INTO engagement (id, state) VALUES ($1, 'OPEN')`, cleEngagement)

	mustExecScoped(`INSERT INTO work_item (id, created_on, updated_on, created_by, updated_by, number, wso2_id, subject, type, project_id)
		VALUES ($1, $2, $2, 'test', 'test', 'CLE-SR-1', 'CLE-WSO2-SR-1', 'cle service request', 'SERVICE_REQUEST', $3)`,
		cleServiceReq, now, cleProjectID)
	mustExecScoped(`INSERT INTO service_request (id, state) VALUES ($1, 'OPEN')`, cleServiceReq)

	mustExecScoped(`INSERT INTO work_item (id, created_on, updated_on, created_by, updated_by, number, wso2_id, subject, type, project_id)
		VALUES ($1, $2, $2, 'test', 'test', 'CLE-SRA-1', 'CLE-WSO2-SRA-1', 'cle security report analysis', 'SECURITY_REPORT_ANALYSIS', $3)`,
		cleSecReport, now, cleProjectID)
	mustExecScoped(`INSERT INTO security_report_analysis (id, state) VALUES ($1, 'OPEN')`, cleSecReport)
}

func TestCaseLikeExtensionRLSIntegration_MemberSeesAllThreeStrangerSeesNone(t *testing.T) {
	pool := caseStatsPool(t)
	seedCaseLikeExtensionFixture(t, pool)
	repo := repository.NewCaseRepository(repository.NewScoped(pool))

	cases := []struct {
		name string
		id   string
	}{
		{"engagement", cleEngagement},
		{"service_request", cleServiceReq},
		{"security_report_analysis", cleSecReport},
	}

	for _, c := range cases {
		t.Run(c.name+"/member sees it", func(t *testing.T) {
			ctx := context.Background()
			scope := repository.SearchScope{Unrestricted: false, ViewerEmail: cleMember}
			if _, err := repo.GetCaseByID(ctx, c.id, scope); err != nil {
				t.Fatalf("GetCaseByID(%s) as registered project member: unexpected error = %v", c.name, err)
			}
		})
		t.Run(c.name+"/stranger gets not-found", func(t *testing.T) {
			ctx := context.Background()
			scope := repository.SearchScope{Unrestricted: false, ViewerEmail: cleStranger}
			_, err := repo.GetCaseByID(ctx, c.id, scope)
			var notFound *apierror.NotFoundError
			if !errors.As(err, &notFound) {
				t.Fatalf("GetCaseByID(%s) as a non-member: err = %v, want *apierror.NotFoundError", c.name, err)
			}
		})
	}
}
