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

package prometheus

import (
	"encoding/json"
	"testing"
)

func alertmanagerPayload(status string, alerts []map[string]any) []byte {
	body := map[string]any{
		"receiver": "default",
		"status":   status,
		"alerts":   alerts,
	}
	raw, _ := json.Marshal(body)
	return raw
}

func sampleAlert(overrides map[string]any) map[string]any {
	a := map[string]any{
		"status": "firing",
		"labels": map[string]any{
			"alertname": "HighCPU",
			"severity":  "critical",
			"service":   "client-example-alert-integration",
			"category":  "service_interruption",
			"cluster":   "prod-1",
		},
		"annotations": map[string]any{
			"summary": "CPU is high",
		},
		"fingerprint": "abc123fingerprint",
	}
	for k, v := range overrides {
		a[k] = v
	}
	return a
}

func TestTransform_InvalidStructure(t *testing.T) {
	cases := [][]byte{
		[]byte(`{}`),
		[]byte(`{"alerts":[]}`),
		[]byte(`{"alerts":"not-an-array"}`),
	}
	for _, raw := range cases {
		if _, err := Transform(raw, Config{}); err == nil {
			t.Errorf("Transform(%s) should error on missing/empty alerts", raw)
		}
	}
}

func TestTransform_SingleAlertBasicFields(t *testing.T) {
	raw := alertmanagerPayload("firing", []map[string]any{sampleAlert(nil)})
	alerts, err := Transform(raw, Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if len(alerts) != 1 {
		t.Fatalf("len(alerts) = %d, want 1", len(alerts))
	}
	a := alerts[0]
	if a.Service != "client-example-alert-integration" {
		t.Errorf("Service = %q", a.Service)
	}
	if a.MetricName != "HighCPU" {
		t.Errorf("MetricName = %q, want HighCPU", a.MetricName)
	}
	if a.Severity != "Critical" {
		t.Errorf("Severity = %q, want Critical", a.Severity)
	}
	if a.Category != "service_interruption" {
		t.Errorf("Category = %q", a.Category)
	}
	if a.Environment != "prod-1" {
		t.Errorf("Environment = %q, want prod-1 (from cluster label)", a.Environment)
	}
	if a.Source != "Prometheus" {
		t.Errorf("Source = %q, want Prometheus", a.Source)
	}
	if a.UniqueIdentifier != "abc123fingerprint" {
		t.Errorf("UniqueIdentifier = %q, want the fingerprint", a.UniqueIdentifier)
	}
}

func TestTransform_MultipleAlertsInOneBatch(t *testing.T) {
	raw := alertmanagerPayload("firing", []map[string]any{
		sampleAlert(map[string]any{"fingerprint": "fp-1"}),
		sampleAlert(map[string]any{"fingerprint": "fp-2"}),
		sampleAlert(map[string]any{"fingerprint": "fp-3"}),
	})
	alerts, err := Transform(raw, Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if len(alerts) != 3 {
		t.Fatalf("len(alerts) = %d, want 3", len(alerts))
	}
	for i, want := range []string{"fp-1", "fp-2", "fp-3"} {
		if alerts[i].UniqueIdentifier != want {
			t.Errorf("alerts[%d].UniqueIdentifier = %q, want %q", i, alerts[i].UniqueIdentifier, want)
		}
	}
}

func TestTransform_MixedBatchUsesEachAlertsStatus(t *testing.T) {
	raw := alertmanagerPayload("firing", []map[string]any{
		sampleAlert(map[string]any{"fingerprint": "fp-1"}),
		sampleAlert(map[string]any{"fingerprint": "fp-2", "status": "resolved"}),
	})
	alerts, err := Transform(raw, Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if alerts[0].Severity != "Critical" || alerts[1].Severity != "OK" {
		t.Errorf("severities = %q, %q; want Critical, OK", alerts[0].Severity, alerts[1].Severity)
	}
}

func TestTransform_StatusFallsBackToBatch(t *testing.T) {
	a := sampleAlert(nil)
	delete(a, "status")
	alerts, err := Transform(alertmanagerPayload("resolved", []map[string]any{a}), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if alerts[0].Severity != "OK" {
		t.Errorf("Severity = %q, want OK from the batch status", alerts[0].Severity)
	}
}

func TestTransform_ObjectStatusStillRead(t *testing.T) {
	a := sampleAlert(map[string]any{"status": map[string]any{"state": "resolved"}})
	alerts, err := Transform(alertmanagerPayload("firing", []map[string]any{a}), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if alerts[0].Severity != "OK" {
		t.Errorf("Severity = %q, want OK from status.state", alerts[0].Severity)
	}
}

func TestTransform_SeverityMapping(t *testing.T) {
	cases := []struct {
		sev  string
		want string
	}{
		{"critical", "Critical"}, {"CRITICAL", "Critical"}, {"high", "Major"},
		{"low", "Minor"}, {"warning", "Warning"}, {"informational", "OK"},
	}
	for _, tc := range cases {
		raw := alertmanagerPayload("firing", []map[string]any{
			sampleAlert(map[string]any{"labels": map[string]any{"alertname": "x", "severity": tc.sev}}),
		})
		alerts, err := Transform(raw, Config{})
		if err != nil {
			t.Fatalf("Transform: %v", err)
		}
		if alerts[0].Severity != tc.want {
			t.Errorf("severity %s -> %q, want %q", tc.sev, alerts[0].Severity, tc.want)
		}
	}
}

func TestTransform_UnmappedSeverityPassesThrough(t *testing.T) {
	raw := alertmanagerPayload("firing", []map[string]any{
		sampleAlert(map[string]any{"labels": map[string]any{"alertname": "x", "severity": "page"}}),
	})
	alerts, err := Transform(raw, Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if alerts[0].Severity != "page" {
		t.Errorf("Severity = %q, want page to pass through unmapped", alerts[0].Severity)
	}
}

func TestTransform_GrafanaDetectedViaFolder(t *testing.T) {
	raw := alertmanagerPayload("firing", []map[string]any{
		sampleAlert(map[string]any{
			"labels":      map[string]any{"alertname": "x", "grafana_folder": "SRE Alerts"},
			"annotations": map[string]any{"title": "High CPU dashboard alert"},
		}),
	})
	alerts, err := Transform(raw, Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if alerts[0].Source != "Grafana" {
		t.Errorf("Source = %q, want Grafana", alerts[0].Source)
	}
	if alerts[0].MetricName != "Grafana Alert: High CPU dashboard alert" {
		t.Errorf("MetricName = %q, want Grafana Alert: High CPU dashboard alert", alerts[0].MetricName)
	}
}

func TestTransform_GrafanaDetectedViaReceiver(t *testing.T) {
	body := map[string]any{
		"receiver": "grafana-default-email",
		"status":   "firing",
		"alerts":   []map[string]any{sampleAlert(nil)},
	}
	raw, _ := json.Marshal(body)
	alerts, err := Transform(raw, Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if alerts[0].Source != "Grafana" {
		t.Errorf("Source = %q, want Grafana (via receiver match)", alerts[0].Source)
	}
}

func TestTransform_ServiceFallsBackToComponentThenJob(t *testing.T) {
	raw := alertmanagerPayload("firing", []map[string]any{
		sampleAlert(map[string]any{"labels": map[string]any{"alertname": "x", "component": "billing-svc"}}),
	})
	alerts, err := Transform(raw, Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if alerts[0].Service != "billing-svc" {
		t.Errorf("Service = %q, want billing-svc (from component label)", alerts[0].Service)
	}
}

func TestTransform_DefaultsWhenNothingProvided(t *testing.T) {
	raw := alertmanagerPayload("firing", []map[string]any{{"labels": map[string]any{}}})
	alerts, err := Transform(raw, Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	a := alerts[0]
	if a.MetricName != "PrometheusAlert" {
		t.Errorf("MetricName = %q, want PrometheusAlert", a.MetricName)
	}
	if a.Service != "Managed Services" {
		t.Errorf("Service = %q, want Managed Services", a.Service)
	}
	if a.Category != "Software" {
		t.Errorf("Category = %q, want Software", a.Category)
	}
	if a.Environment != "Production" {
		t.Errorf("Environment = %q, want Production", a.Environment)
	}
	if a.Severity != "Critical" {
		t.Errorf("Severity = %q, want Critical", a.Severity)
	}
}

func TestTransform_DescriptionIsWholeBatchForEveryAlert(t *testing.T) {
	raw := alertmanagerPayload("firing", []map[string]any{
		sampleAlert(map[string]any{"fingerprint": "fp-1"}),
		sampleAlert(map[string]any{"fingerprint": "fp-2"}),
	})
	alerts, err := Transform(raw, Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if alerts[0].Description != alerts[1].Description {
		t.Error("every alert in a batch should carry the identical, whole-batch description")
	}
}

func TestTransform_ConfigOverridesBeatDefaults(t *testing.T) {
	raw := alertmanagerPayload("firing", []map[string]any{{"labels": map[string]any{}}})
	cfg := Config{"ENVIRONMENT": "staging", "METRIC_NAME": "custom"}
	alerts, err := Transform(raw, cfg)
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if alerts[0].Environment != "staging" {
		t.Errorf("Environment = %q, want staging", alerts[0].Environment)
	}
	if alerts[0].MetricName != "custom" {
		t.Errorf("MetricName = %q, want custom", alerts[0].MetricName)
	}
}
