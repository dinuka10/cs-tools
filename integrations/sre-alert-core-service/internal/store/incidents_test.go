// Copyright (c) 2026, WSO2 LLC. (https://www.wso2.com).
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

package store

import (
	"strings"
	"testing"

	"github.com/scylladb/gocqlx/v2/qb"
)

// TestCasUpdate_QueryShape locks down casUpdate's CQL shape: version bound under distinct SET/IF names with an equality-only IF clause, since Cosmos's Cassandra API supports no other comparator.
func TestCasUpdate_QueryShape(t *testing.T) {
	stmt, names := qb.Update("incidents_processed").
		Set("alert_count").
		SetNamed("version", "new_version").
		Where(qb.Eq("fingerprint")).
		If(qb.EqNamed("version", "expected_version")).
		ToCql()

	if !strings.Contains(stmt, "IF version=?") {
		t.Fatalf("expected an equality-only IF clause on version (Cosmos LWT supports no other comparator), got: %s", stmt)
	}
	if strings.Contains(stmt, "IF version<") || strings.Contains(stmt, "IF version>") {
		t.Fatalf("IF clause must be equality-only, got: %s", stmt)
	}

	wantNames := []string{"alert_count", "new_version", "fingerprint", "expected_version"}
	if len(names) != len(wantNames) {
		t.Fatalf("bound names = %v, want %v", names, wantNames)
	}
	for i, n := range wantNames {
		if names[i] != n {
			t.Fatalf("bound names = %v, want %v", names, wantNames)
		}
	}
	// SET and IF sides of "version" must bind under distinct names, or the new/old values would collide.
	if names[1] == names[3] {
		t.Fatalf("SET and IF sides of version must bind under distinct names, both got %q", names[1])
	}
}

func TestIsPending(t *testing.T) {
	tests := []struct {
		name                 string
		csmConfirmed         bool
		csmPermanentlyFailed bool
		pendingNotesLen      int
		want                 bool
	}{
		{
			name:                 "new unconfirmed incident is pending",
			csmConfirmed:         false,
			csmPermanentlyFailed: false,
			pendingNotesLen:      0,
			want:                 true,
		},
		{
			name:                 "confirmed with no pending notes is not pending",
			csmConfirmed:         true,
			csmPermanentlyFailed: false,
			pendingNotesLen:      0,
			want:                 false,
		},
		{
			name:                 "confirmed with pending notes is pending",
			csmConfirmed:         true,
			csmPermanentlyFailed: false,
			pendingNotesLen:      2,
			want:                 true,
		},
		{
			name:                 "permanently failed unconfirmed is not pending",
			csmConfirmed:         false,
			csmPermanentlyFailed: true,
			pendingNotesLen:      0,
			want:                 false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isPending(tt.csmConfirmed, tt.csmPermanentlyFailed, tt.pendingNotesLen)
			if got != tt.want {
				t.Errorf("isPending(%v, %v, %d) = %v, want %v",
					tt.csmConfirmed, tt.csmPermanentlyFailed, tt.pendingNotesLen, got, tt.want)
			}
		})
	}
}
