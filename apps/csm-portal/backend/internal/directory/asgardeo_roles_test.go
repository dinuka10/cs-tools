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

package directory

import (
	"strings"
	"testing"
)

func TestParseAsgardeoRoleIDs_EmptyIsLegal(t *testing.T) {
	ids, err := ParseAsgardeoRoleIDs("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("got %d entries, want 0", len(ids))
	}
}

func TestParseAsgardeoRoleIDs_ParsesRows(t *testing.T) {
	ids, err := ParseAsgardeoRoleIDs("timecard_approver|0bbeea4f-5ada-49ba-8f19-90ae6a116daa,worknote_creator|11111111-1111-1111-1111-111111111111")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ids["timecard_approver"] != "0bbeea4f-5ada-49ba-8f19-90ae6a116daa" {
		t.Errorf("timecard_approver = %q", ids["timecard_approver"])
	}
	if ids["worknote_creator"] != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("worknote_creator = %q", ids["worknote_creator"])
	}
}

// A blank row is skipped, so a trailing comma is not a deploy failure --
// mirrors TestParseTeamRegistry_TolerantOfBlankRows.
func TestParseAsgardeoRoleIDs_TolerantOfBlankRows(t *testing.T) {
	ids, err := ParseAsgardeoRoleIDs("timecard_approver|role-1,, ,")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 1 || ids["timecard_approver"] != "role-1" {
		t.Fatalf("ids = %+v, want only timecard_approver -> role-1", ids)
	}
}

// Whitespace around a field survives a copy-paste into a configuration form --
// mirrors TestParseTeamRegistry_TrimsWhitespace.
func TestParseAsgardeoRoleIDs_TrimsWhitespace(t *testing.T) {
	ids, err := ParseAsgardeoRoleIDs(" timecard_approver | role-1 ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ids["timecard_approver"] != "role-1" {
		t.Fatalf("ids = %+v, want timecard_approver -> role-1 with no stray whitespace", ids)
	}
}

func TestParseAsgardeoRoleIDs_BadRowsNameTheOffender(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantRow string
	}{
		{"missing id", "timecard_approver", "timecard_approver"},
		{"too many fields", "timecard_approver|role-1|extra", "extra"},
		{"empty role key", "|role-1", "|role-1"},
		{"empty role id", "timecard_approver|", "timecard_approver|"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseAsgardeoRoleIDs(tc.raw)
			if err == nil {
				t.Fatalf("ParseAsgardeoRoleIDs(%q) = nil error, want a failure", tc.raw)
			}
			if !strings.Contains(err.Error(), tc.wantRow) {
				t.Errorf("error %q does not name the offending row %q", err, tc.wantRow)
			}
		})
	}
}

func TestParseAsgardeoRoleIDs_RejectsDuplicateKey(t *testing.T) {
	_, err := ParseAsgardeoRoleIDs("timecard_approver|role-1,timecard_approver|role-2")
	if err == nil {
		t.Fatal("expected an error for a duplicate roleKey, got nil")
	}
	if !strings.Contains(err.Error(), "timecard_approver") {
		t.Errorf("error %q does not name the duplicated key", err)
	}
}
