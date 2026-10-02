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
// KIND, either express or implied. See the License for the
// specific language governing permissions and limitations
// under the License.

package apierror

import "testing"

func TestInvalidValue(t *testing.T) {
	if got := InvalidValue("sortBy", "x", "sort field", []string{"endDate"}).Msg; got != `sortBy: "x" is not a valid sort field; use endDate` {
		t.Errorf("single = %q", got)
	}
	if got := InvalidValue("f", "a\"b", "thing", []string{"A", "B"}).Msg; got != `f: "a\"b" is not a valid thing; use one of A, B` {
		t.Errorf("multi = %q", got)
	}
}
