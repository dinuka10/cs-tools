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

package site24x7

import (
	"encoding/json"
	"testing"
)

func samplePayload(overrides map[string]any) []byte {
	fields := map[string]any{
		"STATUS":      "DOWN",
		"MONITORNAME": "Global-Error-Threshold (Site24x7)",
		"MONITOR_ID":  "mon-987",
		"TAGS":        []string{"svc:client-example-alert-integration", "cat:service_interruption", "env:production"},
	}
	for k, v := range overrides {
		fields[k] = v
	}
	body, _ := json.Marshal(fields)
	return body
}

func tagConfig() Config {
	return Config{
		TagList: map[string]string{
			"Service":     "svc",
			"Category":    "cat",
			"Environment": "env",
		},
	}
}

func TestTransform_SeverityMapping(t *testing.T) {
	cases := []struct {
		status string
		want   string
	}{
		{"DOWN", "Critical"}, {"CRITICAL", "Major"}, {"TROUBLE", "Minor"}, {"UP", "OK"},
	}
	for _, tc := range cases {
		a, err := Transform(samplePayload(map[string]any{"STATUS": tc.status}), Config{})
		if err != nil {
			t.Fatalf("Transform: %v", err)
		}
		if a.Severity != tc.want {
			t.Errorf("status %s -> %q, want %q", tc.status, a.Severity, tc.want)
		}
	}
}

func TestTransform_UnmappedStatusFallsBackToConfiguredDefault(t *testing.T) {
	cfg := Config{Defaults: map[string]string{"Severity": "Warning"}}
	a, err := Transform(samplePayload(map[string]any{"STATUS": "MAINTENANCE"}), cfg)
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Severity != "Warning" {
		t.Errorf("Severity = %q, want Warning (configured default, not the raw status passed through)", a.Severity)
	}
}

func TestTransform_UnmappedStatusWithNoDefaultIsEmpty(t *testing.T) {
	a, err := Transform(samplePayload(map[string]any{"STATUS": "MAINTENANCE"}), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Severity != "" {
		t.Errorf("Severity = %q, want empty when neither the status maps nor a default is configured", a.Severity)
	}
}

func TestTransform_TagExtractionWithConfiguredPrefixes(t *testing.T) {
	a, err := Transform(samplePayload(nil), tagConfig())
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Service != "client-example-alert-integration" {
		t.Errorf("Service = %q, want client-example-alert-integration", a.Service)
	}
	if a.Category != "service_interruption" {
		t.Errorf("Category = %q, want service_interruption", a.Category)
	}
	if a.Environment != "production" {
		t.Errorf("Environment = %q, want production", a.Environment)
	}
}

func TestTransform_NoTagsExtractedWithoutConfiguredTagList(t *testing.T) {
	// No TagList configured at all -- unlike every other vendor, there is
	// no hardcoded set of tag aliases to fall back to.
	a, err := Transform(samplePayload(nil), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Service != "" {
		t.Errorf("Service = %q, want empty (no TagList configured, no hardcoded default)", a.Service)
	}
}

func TestTransform_DefaultsUsedWhenTagsDoNotMatch(t *testing.T) {
	cfg := Config{
		TagList:  map[string]string{"Service": "svc"},
		Defaults: map[string]string{"Service": "managed_services", "Category": "service_interruption"},
	}
	a, err := Transform(samplePayload(map[string]any{"TAGS": []string{}}), cfg)
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Service != "managed_services" {
		t.Errorf("Service = %q, want managed_services (config default)", a.Service)
	}
	if a.Category != "service_interruption" {
		t.Errorf("Category = %q, want service_interruption (config default)", a.Category)
	}
}

func TestTransform_TagWithMultipleColonsLosesEverythingAfterSecondSegment(t *testing.T) {
	a, err := Transform(samplePayload(map[string]any{"TAGS": []string{"svc:my:service"}}), tagConfig())
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Service != "my" {
		t.Errorf("Service = %q, want %q (reference script's split(\":\")[1] quirk)", a.Service, "my")
	}
}

func TestTransform_MetricNameIsMonitorName(t *testing.T) {
	a, err := Transform(samplePayload(nil), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.MetricName != "Global-Error-Threshold (Site24x7)" {
		t.Errorf("MetricName = %q, want Global-Error-Threshold (Site24x7)", a.MetricName)
	}
}

func TestTransform_MetricNameHasNoFallback(t *testing.T) {
	a, err := Transform(samplePayload(map[string]any{"MONITORNAME": ""}), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.MetricName != "" {
		t.Errorf("MetricName = %q, want empty (no config/default fallback for metric_name)", a.MetricName)
	}
}

func TestTransform_UniqueIdentifierIsMonitorID(t *testing.T) {
	a, err := Transform(samplePayload(nil), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.UniqueIdentifier != "mon-987" {
		t.Errorf("UniqueIdentifier = %q, want mon-987", a.UniqueIdentifier)
	}
}

func TestTransform_SourceIsAlwaysSite24x7(t *testing.T) {
	a, err := Transform(samplePayload(nil), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Source != "Site24x7" {
		t.Errorf("Source = %q, want Site24x7", a.Source)
	}
}

func TestTransform_MissingBody(t *testing.T) {
	if _, err := Transform([]byte(""), Config{}); err == nil {
		t.Fatal("Transform(\"\") should error with a missing-body error")
	}
}

func TestTransform_MissingStatusWithNoConfiguredDefault(t *testing.T) {
	if _, err := Transform(samplePayload(map[string]any{"STATUS": ""}), Config{}); err == nil {
		t.Fatal("Transform with no STATUS and no configured default severity should error")
	}
}

func TestTransform_MissingStatusFallsBackToConfiguredDefaultSeverity(t *testing.T) {
	cfg := Config{Defaults: map[string]string{"Severity": "Warning"}}
	a, err := Transform(samplePayload(map[string]any{"STATUS": ""}), cfg)
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	// "Warning" itself isn't a recognized STATUS key (DOWN/CRITICAL/TROUBLE/UP),
	// so it falls through mapSeverity's own default fallback right back to
	// the same configured value.
	if a.Severity != "Warning" {
		t.Errorf("Severity = %q, want Warning", a.Severity)
	}
}
