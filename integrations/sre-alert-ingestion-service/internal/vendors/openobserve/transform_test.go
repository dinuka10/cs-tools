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

package openobserve

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func samplePayload(overrides map[string]string) []byte {
	fields := map[string]string{
		"short_description": "High CPU on client-example",
		"description":       "CPU usage exceeded 90% for 5 minutes",
		"urgency":           "1",
		"impact":            "2",
		"correlation_id":    "cpu-alert-xyz",
		"caller_id":         "openobserve",
		"service":           "client-example-alert-integration",
		"category":          "service_interruption",
		"environment":       "production",
	}
	for k, v := range overrides {
		fields[k] = v
	}
	body, _ := json.Marshal(fields)
	return body
}

func TestTransform_SeverityFromUrgency(t *testing.T) {
	cases := []struct {
		urgency string
		want    string
	}{
		{"1", "Critical"}, {"2", "Major"}, {"3", "Minor"},
	}
	for _, tc := range cases {
		a, err := Transform(samplePayload(map[string]string{"urgency": tc.urgency}), Config{})
		if err != nil {
			t.Fatalf("Transform: %v", err)
		}
		if a.Severity != tc.want {
			t.Errorf("urgency %s -> %q, want %q", tc.urgency, a.Severity, tc.want)
		}
	}
}

func TestTransform_SeverityFallsBackToImpact(t *testing.T) {
	a, err := Transform(samplePayload(map[string]string{"urgency": "9", "impact": "2"}), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Severity != "Major" {
		t.Errorf("Severity = %q, want Major (unmapped urgency should fall back to impact)", a.Severity)
	}
}

func TestTransform_SeverityEmptyWhenNeitherMaps(t *testing.T) {
	a, err := Transform(samplePayload(map[string]string{"urgency": "9", "impact": "9"}), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Severity != "" {
		t.Errorf("Severity = %q, want empty when neither urgency nor impact maps", a.Severity)
	}
}

func TestTransform_MetricNameIsShortDescription(t *testing.T) {
	a, err := Transform(samplePayload(nil), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.MetricName != "High CPU on client-example" {
		t.Errorf("MetricName = %q, want the short_description", a.MetricName)
	}
}

func TestTransform_MetricNameFromCorrelationIDWhenNoShortDescription(t *testing.T) {
	a, err := Transform(samplePayload(map[string]string{"short_description": ""}), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.MetricName != "OpenObserve Alert: cpu-alert-xyz" {
		t.Errorf("MetricName = %q, want OpenObserve Alert: cpu-alert-xyz", a.MetricName)
	}
}

func TestTransform_ShortDescriptionFallsBackToMetricName(t *testing.T) {
	a, err := Transform(samplePayload(map[string]string{"short_description": ""}), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.ShortDescription != a.MetricName {
		t.Errorf("ShortDescription = %q, want it to equal MetricName (%q) when short_description is empty", a.ShortDescription, a.MetricName)
	}
}

func TestTransform_UniqueIdentifierIsCorrelationID(t *testing.T) {
	a, err := Transform(samplePayload(nil), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.UniqueIdentifier != "cpu-alert-xyz" {
		t.Errorf("UniqueIdentifier = %q, want cpu-alert-xyz", a.UniqueIdentifier)
	}
}

func compact(t *testing.T, raw []byte) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestTransform_DescriptionIsTextThenRawPayload(t *testing.T) {
	raw := samplePayload(nil)
	a, err := Transform(raw, Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	want := "CPU usage exceeded 90% for 5 minutes\n\nRaw payload: " + compact(t, raw)
	if a.Description != want {
		t.Errorf("Description = %q, want %q", a.Description, want)
	}
	for _, field := range []string{`"urgency":"1"`, `"impact":"2"`, `"caller_id":"openobserve"`} {
		if !strings.Contains(a.Description, field) {
			t.Errorf("Description should keep %s from the payload", field)
		}
	}
}

func TestTransform_DescriptionWithoutTextIsRawPayloadOnly(t *testing.T) {
	for _, raw := range [][]byte{
		samplePayload(map[string]string{"description": ""}),
		[]byte(`{"correlation_id":"abc","urgency":"1"}`),
	} {
		a, err := Transform(raw, Config{})
		if err != nil {
			t.Fatalf("Transform: %v", err)
		}
		if want := "Raw payload: " + compact(t, raw); a.Description != want {
			t.Errorf("Description = %q, want %q", a.Description, want)
		}
	}
}

func TestTransform_DescriptionKeepsLargeNumbersExact(t *testing.T) {
	a, err := Transform([]byte(`{"short_description":"s","correlation_id":12345678901234567890}`), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if !strings.Contains(a.Description, `"correlation_id":12345678901234567890`) {
		t.Errorf("Description = %q, want the correlation_id unchanged", a.Description)
	}
}

func TestTransform_UrgencyImpactCallerIDDefaults(t *testing.T) {
	raw := []byte(`{"correlation_id":"abc"}`)
	a, err := Transform(raw, Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Urgency != "3" {
		t.Errorf("Urgency = %q, want 3", a.Urgency)
	}
	if a.Impact != "3" {
		t.Errorf("Impact = %q, want 3", a.Impact)
	}
	if a.CallerID != "openobserve" {
		t.Errorf("CallerID = %q, want openobserve", a.CallerID)
	}
	if a.Service != "managed_services" {
		t.Errorf("Service = %q, want managed_services", a.Service)
	}
	if a.Category != "service_interruption" {
		t.Errorf("Category = %q, want service_interruption", a.Category)
	}
	if a.Environment != "production" {
		t.Errorf("Environment = %q, want production", a.Environment)
	}
}

func TestTransform_ConfigOverridesBeatDefaults(t *testing.T) {
	raw := []byte(`{"correlation_id":"abc"}`)
	cfg := Config{"ENVIRONMENT": "staging", "URGENCY": "2"}
	a, err := Transform(raw, cfg)
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Environment != "staging" {
		t.Errorf("Environment = %q, want staging", a.Environment)
	}
	if a.Urgency != "2" {
		t.Errorf("Urgency = %q, want 2", a.Urgency)
	}
}

func TestTransform_CorrelationIDAloneIsValid(t *testing.T) {
	raw := []byte(`{"correlation_id":"abc"}`)
	if _, err := Transform(raw, Config{}); err != nil {
		t.Fatalf("Transform: %v", err)
	}
}

func TestTransform_ShortDescriptionAloneIsValid(t *testing.T) {
	raw := []byte(`{"short_description":"Something happened"}`)
	if _, err := Transform(raw, Config{}); err != nil {
		t.Fatalf("Transform: %v", err)
	}
}

func TestTransform_InvalidStructure(t *testing.T) {
	if _, err := Transform([]byte(`{}`), Config{}); err == nil {
		t.Fatal("Transform({}) should error on missing short_description/correlation_id")
	}
}

func TestTransform_SourceIsOpenObserve(t *testing.T) {
	a, err := Transform(samplePayload(nil), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Source != "OpenObserve" {
		t.Errorf("Source = %q, want OpenObserve", a.Source)
	}
}
