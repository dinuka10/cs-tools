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

package sweep

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// pagedContacts serves a contacts search the way the live API does
// (confirmed against staging): 20 rows when no limit is sent, at most 50,
// honouring offset, and reporting total but no hasMore. row builds the JSON
// object for the i-th contact.
func pagedContacts(t *testing.T, total int, row func(i int) map[string]any) func(body []byte) ([]byte, error) {
	return func(body []byte) ([]byte, error) {
		var req struct {
			Pagination struct {
				Limit  int `json:"limit"`
				Offset int `json:"offset"`
			} `json:"pagination"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("contacts search body %s: %v", body, err)
		}
		limit := req.Pagination.Limit
		if limit == 0 {
			limit = 20
		}
		if limit > 50 {
			return nil, fmt.Errorf("limit %d over the live maximum of 50", limit)
		}
		contacts := []map[string]any{}
		for i := req.Pagination.Offset; i < total && i < req.Pagination.Offset+limit; i++ {
			contacts = append(contacts, row(i))
		}
		return json.Marshal(map[string]any{"contacts": contacts, "total": total, "limit": limit, "offset": req.Pagination.Offset})
	}
}

func customerNoticeEmails(t *testing.T, ntf *mockNotifier) []string {
	t.Helper()
	if len(ntf.sent) != 2 {
		t.Fatalf("ntf.sent = %d, want 2 (internal + customer)", len(ntf.sent))
	}
	var emails []string
	for _, c := range ntf.sent[1].Recipients.Customers {
		emails = append(emails, c.Email)
	}
	return emails
}

func sevenDayProject() (time.Time, project) {
	now := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	endDate := now.AddDate(0, 0, 6)
	return now, project{ID: "p1", Name: "Acme - Subscription", Account: &projectAccountRef{ID: "a1"}, EndDate: &endDate}
}

// TestProcessProject_CustomerNoticeGoesToEveryBusinessContact: every
// business contact on the project is on the one customer notice.
func TestProcessProject_CustomerNoticeGoesToEveryBusinessContact(t *testing.T) {
	reader := &mockEntityReader{
		searchProjectContactsFn: func(ctx context.Context, projectID string, body []byte) ([]byte, error) {
			return []byte(`{"contacts":[
				{"name":"Bob","email":"bob@customer.example","roles":["business_contact"]},
				{"name":"Frank","email":"frank@customer.example","roles":["business_contact"]}
			],"total":2}`), nil
		},
	}
	ntf := &mockNotifier{}
	now, proj := sevenDayProject()

	if err := processProject(context.Background(), reader, &mockProjectUpdater{}, ntf, now, proj); err != nil {
		t.Fatalf("processProject() error = %v, want nil", err)
	}

	got := customerNoticeEmails(t, ntf)
	if len(got) != 2 || got[0] != "bob@customer.example" || got[1] != "frank@customer.example" {
		t.Errorf("customer notice recipients = %v, want [bob@customer.example frank@customer.example]", got)
	}
}

// TestProcessProject_ReadsEveryPageOfProjectContacts: the contacts search
// returns at most one page (20 by default, 50 at most), so a business
// contact past the first page must still be found.
func TestProcessProject_ReadsEveryPageOfProjectContacts(t *testing.T) {
	const total = 120
	serve := pagedContacts(t, total, func(i int) map[string]any {
		return map[string]any{"name": fmt.Sprintf("C%d", i), "email": fmt.Sprintf("c%d@customer.example", i), "roles": []string{"business_contact"}}
	})
	reader := &mockEntityReader{
		searchProjectContactsFn: func(ctx context.Context, projectID string, body []byte) ([]byte, error) { return serve(body) },
	}
	ntf := &mockNotifier{}
	now, proj := sevenDayProject()

	if err := processProject(context.Background(), reader, &mockProjectUpdater{}, ntf, now, proj); err != nil {
		t.Fatalf("processProject() error = %v, want nil", err)
	}

	got := customerNoticeEmails(t, ntf)
	if len(got) != total {
		t.Fatalf("customer notice recipients = %d, want %d", len(got), total)
	}
	if got[total-1] != fmt.Sprintf("c%d@customer.example", total-1) {
		t.Errorf("last recipient = %q, want the last contact on the last page", got[total-1])
	}
}

// TestProcessProject_ReadsEveryPageOfAccountContacts: same for the account
// contacts search behind the Primary Contact fallback.
func TestProcessProject_ReadsEveryPageOfAccountContacts(t *testing.T) {
	const total = 75
	serve := pagedContacts(t, total, func(i int) map[string]any {
		return map[string]any{"name": fmt.Sprintf("P%d", i), "email": fmt.Sprintf("p%d@customer.example", i), "isPrimary": i == total-1}
	})
	reader := &mockEntityReader{
		searchAccountContactsFn: func(ctx context.Context, accountID string, body []byte) ([]byte, error) { return serve(body) },
	}
	ntf := &mockNotifier{}
	now, proj := sevenDayProject()

	if err := processProject(context.Background(), reader, &mockProjectUpdater{}, ntf, now, proj); err != nil {
		t.Fatalf("processProject() error = %v, want nil", err)
	}

	got := customerNoticeEmails(t, ntf)
	want := fmt.Sprintf("p%d@customer.example", total-1)
	if len(got) != 1 || got[0] != want {
		t.Errorf("customer notice recipients = %v, want [%s] (the only primary contact, on the last page)", got, want)
	}
}
