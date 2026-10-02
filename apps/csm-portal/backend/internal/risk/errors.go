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

package risk

import "errors"

// errRecordNotFound is returned by internal row lookups (a risk, action
// item, health status row, or comment that doesn't exist). It is
// deliberately a plain, unexported error rather than a typed "not found"
// error: in the Ballerina source, these same lookups (getRiskById,
// getActionItemRowById, getHealthStatusByProject, getCommentById) return a
// plain `error(...)`, not a typed SupportLiteNotFound — so it propagates up
// through every caller as a generic failure and the service.bal resource
// function's `on fail error err` catch-all turns it into a 500 Internal
// Server Error, not a 404. This port preserves that exact (if surprising)
// behavior rather than "fixing" it into a 404, since that would be an
// observable API change beyond what this migration is meant to make —
// handlers should map any error that isn't a *ValidationError to a generic
// 500 via mapUpstreamErrorGeneric-style handling, which happens naturally
// if this stays untyped.
var errRecordNotFound = errors.New("risk: record not found")

// ValidationError is returned for the specific cases the Ballerina source
// types as utils:SupportLiteBadRequest — caller-actionable input problems a
// handler should surface as 400 Bad Request with this exact message
// (operator-authored text, safe to echo to the caller, unlike an upstream
// error body).
type ValidationError struct {
	Message string
}

func (e *ValidationError) Error() string {
	return e.Message
}
