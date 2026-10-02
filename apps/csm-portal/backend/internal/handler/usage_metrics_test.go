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

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wso2-open-operations/cs-tools/apps/csm-portal/backend/internal/apierror"
	"github.com/wso2-open-operations/cs-tools/apps/csm-portal/backend/internal/middleware"
)

// mockUsageMetricsClient is a test double for usageMetricsServiceNowClient.
type mockUsageMetricsClient struct {
	response []byte
	err      error

	lastSearch            string
	lastPayload           []byte
	lastDeployedProductID string
}

func (m *mockUsageMetricsClient) GetAllProjects(ctx context.Context, search string) ([]byte, error) {
	m.lastSearch = search
	return m.response, m.err
}
func (m *mockUsageMetricsClient) SearchInstanceMetrics(ctx context.Context, payload []byte) ([]byte, error) {
	m.lastPayload = payload
	return m.response, m.err
}
func (m *mockUsageMetricsClient) GetInstanceMetricsStats(ctx context.Context, payload []byte) ([]byte, error) {
	m.lastPayload = payload
	return m.response, m.err
}
func (m *mockUsageMetricsClient) SearchInstanceUsages(ctx context.Context, payload []byte) ([]byte, error) {
	m.lastPayload = payload
	return m.response, m.err
}
func (m *mockUsageMetricsClient) GetInstanceUsagesStats(ctx context.Context, payload []byte) ([]byte, error) {
	m.lastPayload = payload
	return m.response, m.err
}
func (m *mockUsageMetricsClient) SearchUsageMetricsDeployments(ctx context.Context, payload []byte) ([]byte, error) {
	m.lastPayload = payload
	return m.response, m.err
}
func (m *mockUsageMetricsClient) SearchUsageMetricsProjects(ctx context.Context, payload []byte) ([]byte, error) {
	m.lastPayload = payload
	return m.response, m.err
}
func (m *mockUsageMetricsClient) SearchUsageMetricsDeployedProducts(ctx context.Context, payload []byte) ([]byte, error) {
	m.lastPayload = payload
	return m.response, m.err
}
func (m *mockUsageMetricsClient) SearchUsageMetricsInstances(ctx context.Context, payload []byte) ([]byte, error) {
	m.lastPayload = payload
	return m.response, m.err
}
func (m *mockUsageMetricsClient) GetDeployedProductMetrics(ctx context.Context, deployedProductID string, payload []byte) ([]byte, error) {
	m.lastDeployedProductID = deployedProductID
	m.lastPayload = payload
	return m.response, m.err
}
func (m *mockUsageMetricsClient) GetDeployedProductUsageCounts(ctx context.Context, deployedProductID string, payload []byte) ([]byte, error) {
	m.lastDeployedProductID = deployedProductID
	m.lastPayload = payload
	return m.response, m.err
}

func TestUsageMetricsHandler_GetProjects_RequiresAuth(t *testing.T) {
	h := NewUsageMetricsHandler(&mockUsageMetricsClient{}, viewerAccessGuard)

	req := httptest.NewRequest(http.MethodGet, "/spl/usage-metrics/projects", nil)
	w := httptest.NewRecorder()
	h.GetProjects(w, req)

	assertStatus(t, w, http.StatusUnauthorized)
}

func TestUsageMetricsHandler_GetProjects_RequiresUsageMetricsPermission(t *testing.T) {
	h := NewUsageMetricsHandler(&mockUsageMetricsClient{}, viewerAccessGuard)

	// SPL access (sales_solutions) but no usage_metrics_viewer/cs_engineer/
	// admin — passes PermViewerAccess, fails the additional
	// PermUsageMetricsViewer check.
	req := httptest.NewRequest(http.MethodGet, "/spl/usage-metrics/projects", nil)
	req = req.WithContext(middleware.WithUserInfo(req.Context(), &middleware.UserInfo{
		Email: "sales@example.com", UserID: "u-sales", Roles: []string{"test-sales-solutions"},
	}))
	w := httptest.NewRecorder()
	h.GetProjects(w, req)

	assertStatus(t, w, http.StatusForbidden)
}

func TestUsageMetricsHandler_GetProjects_Success(t *testing.T) {
	client := &mockUsageMetricsClient{response: []byte(`[{"sys_id":"1"}]`)}
	h := NewUsageMetricsHandler(client, viewerAccessGuard)

	req := withUser(httptest.NewRequest(http.MethodGet, "/spl/usage-metrics/projects?search=Acme", nil))
	w := httptest.NewRecorder()
	h.GetProjects(w, req)

	assertStatus(t, w, http.StatusOK)
	if client.lastSearch != "Acme" {
		t.Errorf("client received search = %q, want %q", client.lastSearch, "Acme")
	}
	if w.Body.String() != `[{"sys_id":"1"}]` {
		t.Errorf("body = %s, want raw passthrough", w.Body.String())
	}
}

func TestUsageMetricsHandler_GetProjects_MapsUpstreamError(t *testing.T) {
	client := &mockUsageMetricsClient{err: &apierror.Error{StatusCode: http.StatusServiceUnavailable, Body: "down"}}
	h := NewUsageMetricsHandler(client, viewerAccessGuard)

	req := withUser(httptest.NewRequest(http.MethodGet, "/spl/usage-metrics/projects", nil))
	w := httptest.NewRecorder()
	h.GetProjects(w, req)

	assertStatus(t, w, http.StatusServiceUnavailable)
}

func TestUsageMetricsHandler_SearchInstanceMetrics_ForwardsBody(t *testing.T) {
	client := &mockUsageMetricsClient{response: []byte(`{"ok":true}`)}
	h := NewUsageMetricsHandler(client, viewerAccessGuard)

	body := `{"projectIds":["p1"]}`
	req := withUser(httptest.NewRequest(http.MethodPost, "/spl/usage-metrics/instances/metrics/search", strings.NewReader(body)))
	w := httptest.NewRecorder()
	h.SearchInstanceMetrics(w, req)

	assertStatus(t, w, http.StatusOK)
	if string(client.lastPayload) != body {
		t.Errorf("forwarded payload = %s, want %s", client.lastPayload, body)
	}
}

func TestUsageMetricsHandler_SearchInstanceMetrics_RejectsInvalidJSON(t *testing.T) {
	client := &mockUsageMetricsClient{}
	h := NewUsageMetricsHandler(client, viewerAccessGuard)

	req := withUser(httptest.NewRequest(http.MethodPost, "/spl/usage-metrics/instances/metrics/search", strings.NewReader("not json")))
	w := httptest.NewRecorder()
	h.SearchInstanceMetrics(w, req)

	assertStatus(t, w, http.StatusBadRequest)
	if client.lastPayload != nil {
		t.Error("upstream client should not have been called for invalid JSON")
	}
}

func TestUsageMetricsHandler_GetDeployedProductMetrics_RejectsInvalidDateRange(t *testing.T) {
	client := &mockUsageMetricsClient{response: []byte(`{}`)}
	h := NewUsageMetricsHandler(client, viewerAccessGuard)

	body := `{"deploymentId":"d1","startDate":"2026-06-01","endDate":"2026-01-01"}`
	req := withUser(httptest.NewRequest(http.MethodPost, "/spl/usage-metrics/deployed-products/dp-1/metrics/search", strings.NewReader(body)))
	req.SetPathValue("id", "dp-1")
	w := httptest.NewRecorder()
	h.GetDeployedProductMetrics(w, req)

	assertStatus(t, w, http.StatusBadRequest)
	if client.lastPayload != nil {
		t.Error("upstream client should not have been called for an invalid date range")
	}
}

func TestUsageMetricsHandler_GetDeployedProductMetrics_Success(t *testing.T) {
	client := &mockUsageMetricsClient{response: []byte(`{"summary":{}}`)}
	h := NewUsageMetricsHandler(client, viewerAccessGuard)

	body := `{"deploymentId":"d1","startDate":"2026-01-01","endDate":"2026-01-31"}`
	req := withUser(httptest.NewRequest(http.MethodPost, "/spl/usage-metrics/deployed-products/dp-1/metrics/search", strings.NewReader(body)))
	req.SetPathValue("id", "dp-1")
	w := httptest.NewRecorder()
	h.GetDeployedProductMetrics(w, req)

	assertStatus(t, w, http.StatusOK)
	if client.lastDeployedProductID != "dp-1" {
		t.Errorf("deployedProductID = %q, want %q", client.lastDeployedProductID, "dp-1")
	}
	if string(client.lastPayload) != body {
		t.Errorf("forwarded payload = %s, want the original raw body %s", client.lastPayload, body)
	}
}

func TestUsageMetricsHandler_GetDeployedProductMetrics_RequiresID(t *testing.T) {
	client := &mockUsageMetricsClient{}
	h := NewUsageMetricsHandler(client, viewerAccessGuard)

	body := `{"deploymentId":"d1","startDate":"2026-01-01","endDate":"2026-01-31"}`
	req := withUser(httptest.NewRequest(http.MethodPost, "/spl/usage-metrics/deployed-products//metrics/search", strings.NewReader(body)))
	w := httptest.NewRecorder()
	h.GetDeployedProductMetrics(w, req)

	assertStatus(t, w, http.StatusBadRequest)
}
