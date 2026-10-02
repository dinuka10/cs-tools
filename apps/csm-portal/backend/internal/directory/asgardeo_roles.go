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
	"fmt"
	"strings"
)

// ParseAsgardeoRoleIDs parses a role-key -> Asgardeo role ID mapping from its
// configuration form: a comma-separated list of "roleKey|asgardeoRoleId" rows,
// whitespace around each field trimmed. A caller that needs a role's real
// Asgardeo membership (e.g. listing time card approvers via the SCIM
// operations service's get-by-id endpoint) looks its configured id up here by
// key, rather than the role's display name or any other derived value --
// Asgardeo's Roles API v2 takes the role's own id, not a name.
//
// Unlike the team registry and role allow-list, there is no default and no
// requirement that every key be present: which roles have an Asgardeo-backed
// lookup wired up at all is deployment- and feature-specific, so an
// unconfigured key simply has no entry (callers treat that as "this role has
// no SCIM lookup configured," e.g. by not registering the route at all --
// see cmd/server/main.go).
//
// A duplicate roleKey is an error, the same reasoning ParseTeamRegistry's own
// doc comment gives for a duplicate teamKey: a shadowed row is always a typo,
// and failing at startup is cheaper than two keys silently resolving to
// whichever row's map write happened to run last.
func ParseAsgardeoRoleIDs(raw string) (map[string]string, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string]string{}, nil
	}

	rows := strings.Split(raw, ",")
	ids := make(map[string]string, len(rows))

	for i, row := range rows {
		if strings.TrimSpace(row) == "" {
			// Tolerate a trailing or doubled comma.
			continue
		}

		fields := strings.Split(row, "|")
		if len(fields) != 2 {
			return nil, fmt.Errorf(
				"asgardeo role id mapping row %d (%q): expected 2 %q-separated fields (roleKey|asgardeoRoleId), got %d",
				i+1, strings.TrimSpace(row), "|", len(fields))
		}
		key := strings.TrimSpace(fields[0])
		id := strings.TrimSpace(fields[1])

		if key == "" {
			return nil, fmt.Errorf("asgardeo role id mapping row %d (%q): roleKey is empty", i+1, strings.TrimSpace(row))
		}
		if id == "" {
			return nil, fmt.Errorf("asgardeo role id mapping row %d (%q): asgardeoRoleId is empty", i+1, strings.TrimSpace(row))
		}
		if _, dup := ids[key]; dup {
			return nil, fmt.Errorf("asgardeo role id mapping: roleKey %q is configured more than once", key)
		}
		ids[key] = id
	}
	return ids, nil
}
