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

package repository

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestSupportGroupOfServiceLive checks the repository SQL against a real
// database. It seeds its own group and two services -- one with that group as
// its support group, one with none -- so both cases are checked on every run
// whatever the database already holds, and deletes them afterwards.
func TestSupportGroupOfServiceLive(t *testing.T) {
	dsn := os.Getenv("PG_OUTAGE_TEST_DSN")
	if dsn == "" {
		t.Skip("PG_OUTAGE_TEST_DSN not set; this test needs a real database")
	}
	ctx := WithSystemIdentity(context.Background())
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	repo := NewIncidentRepository(NewScoped(pool))

	const (
		group     = "5e5e5e5e-0000-4000-8000-000000000001"
		withGroup = "5e5e5e5e-0000-4000-8000-000000000002"
		without   = "5e5e5e5e-0000-4000-8000-000000000003"
	)
	// Registered before the inserts so a half-seeded run is still cleaned up;
	// services first, since they reference the group.
	defer func() {
		bg := context.Background()
		if _, err := pool.Exec(bg, `DELETE FROM service WHERE id IN ($1::uuid, $2::uuid)`, withGroup, without); err != nil {
			t.Errorf("CLEANUP FAILED, delete services %s, %s by hand: %v", withGroup, without, err)
		}
		if _, err := pool.Exec(bg, `DELETE FROM "group" WHERE id = $1::uuid`, group); err != nil {
			t.Errorf("CLEANUP FAILED, delete group %s by hand: %v", group, err)
		}
	}()
	if _, err := pool.Exec(ctx, `
INSERT INTO "group" (id, created_on, updated_on, created_by, updated_by, name)
VALUES ($1::uuid, now(), now(), 'test', 'test', 'support-group live test')`, group); err != nil {
		t.Fatalf("seed group: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO service (id, created_on, updated_on, created_by, updated_by, name, number, support_group_id)
VALUES ($1::uuid, now(), now(), 'test', 'test', 'support-group live test (with)',    'SVC-TEST-1', $3::uuid),
       ($2::uuid, now(), now(), 'test', 'test', 'support-group live test (without)', 'SVC-TEST-2', NULL)`,
		withGroup, without, group); err != nil {
		t.Fatalf("seed services: %v", err)
	}

	for _, c := range []struct{ name, serviceID, want string }{
		{"service with a support group", withGroup, group},
		{"service without one", without, ""},
		{"unknown service", "00000000-0000-0000-0000-000000000001", ""},
	} {
		if got, err := repo.SupportGroupOfService(ctx, c.serviceID); err != nil || got != c.want {
			t.Errorf("%s: got %q err %v, want %q", c.name, got, err, c.want)
		}
	}
}
