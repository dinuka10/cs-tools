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

package opensearch

import (
	"encoding/json"
	"strings"
	"testing"
)

func samplePayload(overrides map[string]string) []byte {
	fields := map[string]string{
		"monitor_id":   "mon-1",
		"monitor_name": "Global-Error-Threshold (OpenSearch)",
		"trigger_name": "error-rate-trigger",
		"state":        "ACTIVE",
		"alert_id":     "abc123",
		"severity":     "1",
		"service":      "client-example-alert-integration",
		"category":     "service_interruption",
	}
	for k, v := range overrides {
		fields[k] = v
	}
	body, _ := json.Marshal(fields)
	return body
}

func TestTransform_SeverityMapping(t *testing.T) {
	cases := []struct {
		sev  string
		want string
	}{
		{"1", "Critical"}, {"2", "Major"}, {"3", "Minor"}, {"4", "Warning"}, {"5", "OK"},
	}
	for _, tc := range cases {
		a, err := Transform(samplePayload(map[string]string{"severity": tc.sev}), Config{})
		if err != nil {
			t.Fatalf("Transform: %v", err)
		}
		if a.Severity != tc.want {
			t.Errorf("severity %s -> %q, want %q", tc.sev, a.Severity, tc.want)
		}
	}
}

func TestTransform_CompletedForcesOK(t *testing.T) {
	a, err := Transform(samplePayload(map[string]string{"state": "COMPLETED", "severity": "1"}), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Severity != "OK" {
		t.Errorf("Severity = %q, want OK for a COMPLETED state", a.Severity)
	}
}

func TestTransform_ErrorForcesCritical(t *testing.T) {
	a, err := Transform(samplePayload(map[string]string{"state": "ERROR", "severity": "5"}), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Severity != "Critical" {
		t.Errorf("Severity = %q, want Critical for an ERROR state regardless of severity", a.Severity)
	}
}

func TestTransform_EnvironmentNeverFromPayload(t *testing.T) {
	a, err := Transform(samplePayload(map[string]string{"environment": "staging"}), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Environment != "production" {
		t.Errorf("Environment = %q, want production (payload environment must be ignored)", a.Environment)
	}
}

func TestTransform_EnvironmentFromConfig(t *testing.T) {
	a, err := Transform(samplePayload(nil), Config{"ENVIRONMENT": "staging"})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Environment != "staging" {
		t.Errorf("Environment = %q, want staging", a.Environment)
	}
}

func TestTransform_MetricNameJoinsMonitorAndTriggerNames(t *testing.T) {
	a, err := Transform(samplePayload(nil), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	want := "OpenSearch Alert: Global-Error-Threshold (OpenSearch) / error-rate-trigger"
	if a.MetricName != want {
		t.Errorf("MetricName = %q, want %q", a.MetricName, want)
	}
}

func TestTransform_MetricNameFallsBackToDefault(t *testing.T) {
	a, err := Transform(samplePayload(map[string]string{"monitor_name": "", "trigger_name": "", "monitor_id": "mon-1"}), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.MetricName != "OpenSearchAlert" {
		t.Errorf("MetricName = %q, want OpenSearchAlert", a.MetricName)
	}
}

func TestTransform_UniqueIdentifierIsAlertID(t *testing.T) {
	a, err := Transform(samplePayload(nil), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.UniqueIdentifier != "abc123" {
		t.Errorf("UniqueIdentifier = %q, want abc123", a.UniqueIdentifier)
	}
}

func TestTransform_SourceIsOpenSearch(t *testing.T) {
	a, err := Transform(samplePayload(nil), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Source != "OpenSearch" {
		t.Errorf("Source = %q, want OpenSearch", a.Source)
	}
}

func TestTransform_MonitorNameAloneIsValid(t *testing.T) {
	raw := []byte(`{"monitor_id":"","monitor_name":"Some Monitor"}`)
	if _, err := Transform(raw, Config{}); err != nil {
		t.Fatalf("Transform: %v", err)
	}
}

func TestTransform_InvalidStructure(t *testing.T) {
	if _, err := Transform([]byte(`{}`), Config{}); err == nil {
		t.Fatal("Transform({}) should error on missing monitor_id/monitor_name")
	}
}

func TestTransform_DescriptionContainsRawPayload(t *testing.T) {
	a, err := Transform(samplePayload(nil), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if !strings.Contains(a.Description, "mon-1") {
		t.Errorf("Description = %q, want it to contain the raw payload", a.Description)
	}
}
