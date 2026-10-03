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

package gcp

import (
	"encoding/json"
	"testing"
)

func sampleIncident(overrides map[string]any) map[string]any {
	incident := map[string]any{
		"incident_id":    "0.abcd1234",
		"state":          "open",
		"severity":       "critical",
		"policy_name":    "High CPU Policy",
		"condition_name": "CPU usage above 90%",
		"resource": map[string]any{
			"labels": map[string]any{
				"service":     "client-example-alert-integration",
				"category":    "service_interruption",
				"environment": "production",
			},
		},
	}
	for k, v := range overrides {
		incident[k] = v
	}
	return incident
}

func envelope(incident map[string]any) []byte {
	body := map[string]any{"incident": incident, "version": "1.2"}
	raw, _ := json.Marshal(body)
	return raw
}

func TestTransform_SeverityMapping(t *testing.T) {
	cases := []struct {
		sev  string
		want string
	}{
		{"critical", "Critical"}, {"error", "Major"}, {"warning", "Minor"}, {"info", "Informational"},
	}
	for _, tc := range cases {
		a, err := Transform(envelope(sampleIncident(map[string]any{"severity": tc.sev})), Config{})
		if err != nil {
			t.Fatalf("Transform: %v", err)
		}
		if a.Severity != tc.want {
			t.Errorf("severity %s -> %q, want %q", tc.sev, a.Severity, tc.want)
		}
	}
}

func TestTransform_ClosedStateForcesOK(t *testing.T) {
	a, err := Transform(envelope(sampleIncident(map[string]any{"state": "closed", "severity": "critical"})), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Severity != "OK" {
		t.Errorf("Severity = %q, want OK for a closed incident", a.Severity)
	}
}

func TestTransform_StateDefaultsToOpen(t *testing.T) {
	incident := sampleIncident(nil)
	delete(incident, "state")
	a, err := Transform(envelope(incident), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Severity != "Critical" {
		t.Errorf("Severity = %q, want Critical (missing state should default to open, not closed)", a.Severity)
	}
}

func TestTransform_UnmappedSeverityPassesThrough(t *testing.T) {
	a, err := Transform(envelope(sampleIncident(map[string]any{"severity": "page"})), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Severity != "page" {
		t.Errorf("Severity = %q, want page to pass through unmapped", a.Severity)
	}
}

func TestTransform_MetricNameFromPolicyName(t *testing.T) {
	a, err := Transform(envelope(sampleIncident(nil)), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.MetricName != "High CPU Policy" {
		t.Errorf("MetricName = %q, want High CPU Policy", a.MetricName)
	}
}

func TestTransform_MetricNameFallsBackToConditionName(t *testing.T) {
	incident := sampleIncident(map[string]any{"policy_name": ""})
	a, err := Transform(envelope(incident), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.MetricName != "CPU usage above 90%" {
		t.Errorf("MetricName = %q, want CPU usage above 90%%", a.MetricName)
	}
}

func TestTransform_MetricNameFallsBackToDefault(t *testing.T) {
	incident := sampleIncident(map[string]any{"policy_name": "", "condition_name": ""})
	a, err := Transform(envelope(incident), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.MetricName != "GCPCloudAlert" {
		t.Errorf("MetricName = %q, want GCPCloudAlert", a.MetricName)
	}
}

func TestTransform_FieldsFromResourceLabels(t *testing.T) {
	a, err := Transform(envelope(sampleIncident(nil)), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Service != "client-example-alert-integration" {
		t.Errorf("Service = %q", a.Service)
	}
	if a.Category != "service_interruption" {
		t.Errorf("Category = %q", a.Category)
	}
	if a.Environment != "production" {
		t.Errorf("Environment = %q", a.Environment)
	}
}

func TestTransform_FieldsFallBackToPolicyUserLabels(t *testing.T) {
	incident := sampleIncident(map[string]any{
		"resource": map[string]any{"labels": map[string]any{}},
		"policy_user_labels": map[string]any{
			"service":     "user-label-service",
			"category":    "user-label-category",
			"environment": "user-label-env",
		},
	})
	a, err := Transform(envelope(incident), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Service != "user-label-service" {
		t.Errorf("Service = %q, want user-label-service", a.Service)
	}
	if a.Category != "user-label-category" {
		t.Errorf("Category = %q, want user-label-category", a.Category)
	}
	if a.Environment != "user-label-env" {
		t.Errorf("Environment = %q, want user-label-env", a.Environment)
	}
}

func TestTransform_DefaultsWhenNothingProvided(t *testing.T) {
	a, err := Transform(envelope(map[string]any{}), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Service != "Managed Services" {
		t.Errorf("Service = %q, want Managed Services", a.Service)
	}
	if a.Category != "Service Interruption" {
		t.Errorf("Category = %q, want Service Interruption", a.Category)
	}
	if a.Environment != "Unknown" {
		t.Errorf("Environment = %q, want Unknown", a.Environment)
	}
	if a.MetricName != "GCPCloudAlert" {
		t.Errorf("MetricName = %q, want GCPCloudAlert", a.MetricName)
	}
	if a.Severity != "Critical" {
		t.Errorf("Severity = %q, want Critical (default severity round-trips through the map)", a.Severity)
	}
}

func TestTransform_MissingIncidentObjectDegradesGracefully(t *testing.T) {
	raw := []byte(`{"version":"1.2"}`)
	a, err := Transform(raw, Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Service != "Managed Services" {
		t.Errorf("Service = %q, want Managed Services (defaults) when incident is missing", a.Service)
	}
	if a.UniqueIdentifier != "" {
		t.Errorf("UniqueIdentifier = %q, want empty when incident is missing", a.UniqueIdentifier)
	}
}

func TestTransform_UniqueIdentifierIsIncidentID(t *testing.T) {
	a, err := Transform(envelope(sampleIncident(nil)), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.UniqueIdentifier != "0.abcd1234" {
		t.Errorf("UniqueIdentifier = %q, want 0.abcd1234", a.UniqueIdentifier)
	}
}

func TestTransform_SourceIsGCP(t *testing.T) {
	a, err := Transform(envelope(sampleIncident(nil)), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Source != "GCP" {
		t.Errorf("Source = %q, want GCP", a.Source)
	}
}

func TestTransform_ConfigOverridesBeatDefaults(t *testing.T) {
	cfg := Config{"ENVIRONMENT": "staging", "severity": "warning"}
	a, err := Transform(envelope(map[string]any{}), cfg)
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Environment != "staging" {
		t.Errorf("Environment = %q, want staging", a.Environment)
	}
	if a.Severity != "Minor" {
		t.Errorf("Severity = %q, want Minor (config severity default mapped through)", a.Severity)
	}
}

func TestTransform_MissingBody(t *testing.T) {
	if _, err := Transform([]byte(""), Config{}); err == nil {
		t.Fatal("Transform(\"\") should error with a missing-body error")
	}
	if _, err := Transform([]byte("   "), Config{}); err == nil {
		t.Fatal("Transform(whitespace) should error with a missing-body error")
	}
}
