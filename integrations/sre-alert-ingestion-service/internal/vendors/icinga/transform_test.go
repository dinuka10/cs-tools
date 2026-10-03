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

package icinga

import (
	"encoding/json"
	"testing"
)

func serviceCheckPayload(overrides map[string]any) []byte {
	fields := map[string]any{
		"notification_type": "PROBLEM",
		"host_name":         "db-01",
		"host_display_name": "db-01.prod",
		"host_state":        "UP",
		"service_name":      "disk-space",
		"service_state":     "CRITICAL",
	}
	for k, v := range overrides {
		fields[k] = v
	}
	body, _ := json.Marshal(fields)
	return body
}

func hostCheckPayload(overrides map[string]any) []byte {
	fields := map[string]any{
		"notification_type": "PROBLEM",
		"host_name":         "db-01",
		"host_display_name": "db-01.prod",
		"host_state":        "DOWN",
	}
	for k, v := range overrides {
		fields[k] = v
	}
	body, _ := json.Marshal(fields)
	return body
}

func TestTransform_ServiceCheckSeverityMapping(t *testing.T) {
	cases := []struct {
		state string
		want  string
	}{
		{"CRITICAL", "Critical"}, {"WARNING", "Warning"}, {"UNKNOWN", "Major"}, {"OK", "OK"},
	}
	for _, tc := range cases {
		a, err := Transform(serviceCheckPayload(map[string]any{"service_state": tc.state}), Config{})
		if err != nil {
			t.Fatalf("Transform: %v", err)
		}
		if a.Severity != tc.want {
			t.Errorf("service_state %s -> %q, want %q", tc.state, a.Severity, tc.want)
		}
	}
}

func TestTransform_HostCheckSeverityMapping(t *testing.T) {
	cases := []struct {
		state string
		want  string
	}{
		{"DOWN", "Critical"}, {"UNREACHABLE", "Major"}, {"UP", "OK"},
	}
	for _, tc := range cases {
		a, err := Transform(hostCheckPayload(map[string]any{"host_state": tc.state}), Config{})
		if err != nil {
			t.Fatalf("Transform: %v", err)
		}
		if a.Severity != tc.want {
			t.Errorf("host_state %s -> %q, want %q", tc.state, a.Severity, tc.want)
		}
	}
}

func TestTransform_RecoveryForcesOKRegardlessOfState(t *testing.T) {
	a, err := Transform(serviceCheckPayload(map[string]any{"notification_type": "RECOVERY", "service_state": "CRITICAL"}), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Severity != "OK" {
		t.Errorf("Severity = %q, want OK for a RECOVERY notification", a.Severity)
	}
}

func TestTransform_NotificationTypeIsCaseInsensitive(t *testing.T) {
	a, err := Transform(serviceCheckPayload(map[string]any{"notification_type": "recovery"}), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Severity != "OK" {
		t.Errorf("Severity = %q, want OK for a lowercase recovery notification", a.Severity)
	}
}

func TestTransform_UnmappedStatePassesThrough(t *testing.T) {
	a, err := Transform(serviceCheckPayload(map[string]any{"service_state": "PENDING"}), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Severity != "PENDING" {
		t.Errorf("Severity = %q, want PENDING to pass through unmapped", a.Severity)
	}
}

func TestTransform_UniqueIdentifierServiceCheck(t *testing.T) {
	a, err := Transform(serviceCheckPayload(nil), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.UniqueIdentifier != "db-01!disk-space" {
		t.Errorf("UniqueIdentifier = %q, want db-01!disk-space", a.UniqueIdentifier)
	}
}

func TestTransform_UniqueIdentifierHostCheck(t *testing.T) {
	a, err := Transform(hostCheckPayload(nil), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.UniqueIdentifier != "db-01" {
		t.Errorf("UniqueIdentifier = %q, want db-01 (host name only, no service)", a.UniqueIdentifier)
	}
}

func TestTransform_MetricNameServiceCheck(t *testing.T) {
	a, err := Transform(serviceCheckPayload(nil), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.MetricName != "Icinga Alert: db-01.prod / disk-space" {
		t.Errorf("MetricName = %q, want Icinga Alert: db-01.prod / disk-space", a.MetricName)
	}
}

func TestTransform_MetricNameHostCheck(t *testing.T) {
	a, err := Transform(hostCheckPayload(nil), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.MetricName != "Icinga Alert: db-01.prod" {
		t.Errorf("MetricName = %q, want Icinga Alert: db-01.prod", a.MetricName)
	}
}

func TestTransform_HostDisplayNameFallsBackToHostName(t *testing.T) {
	a, err := Transform(hostCheckPayload(map[string]any{"host_display_name": ""}), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.MetricName != "Icinga Alert: db-01" {
		t.Errorf("MetricName = %q, want Icinga Alert: db-01 (host_name fallback)", a.MetricName)
	}
}

func TestTransform_VarsSupplyFieldsBeforeTopLevel(t *testing.T) {
	raw := serviceCheckPayload(map[string]any{
		"vars":        map[string]any{"service": "from-vars", "category": "from-vars-cat", "environment": "from-vars-env"},
		"service":     "from-top-level",
		"category":    "from-top-level-cat",
		"environment": "from-top-level-env",
	})
	a, err := Transform(raw, Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Service != "from-vars" {
		t.Errorf("Service = %q, want from-vars (vars should win over top-level)", a.Service)
	}
	if a.Category != "from-vars-cat" {
		t.Errorf("Category = %q, want from-vars-cat", a.Category)
	}
	if a.Environment != "from-vars-env" {
		t.Errorf("Environment = %q, want from-vars-env", a.Environment)
	}
}

func TestTransform_TopLevelFieldsUsedWhenVarsAbsent(t *testing.T) {
	raw := serviceCheckPayload(map[string]any{"service": "top-level-service"})
	a, err := Transform(raw, Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Service != "top-level-service" {
		t.Errorf("Service = %q, want top-level-service", a.Service)
	}
}

func TestTransform_DefaultsWhenNothingProvided(t *testing.T) {
	raw := []byte(`{"notification_type":"PROBLEM","host_name":"db-01"}`)
	a, err := Transform(raw, Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Service != "managed_services" {
		t.Errorf("Service = %q, want managed_services", a.Service)
	}
	if a.Category != "service_interruption" {
		t.Errorf("Category = %q, want service_interruption", a.Category)
	}
	if a.Environment != "development" {
		t.Errorf("Environment = %q, want development", a.Environment)
	}
	// The METRIC_NAME default can never actually be reached: host_name is
	// mandatory (validation requires it), and hostDisplayName always falls
	// back to it, so buildMetricName's tier-1 value is never empty.
	if a.MetricName != "Icinga Alert: db-01" {
		t.Errorf("MetricName = %q, want Icinga Alert: db-01", a.MetricName)
	}
}

func TestTransform_MetricNameDefaultIsUnreachable(t *testing.T) {
	// Documents the finding directly: there is no payload shape that
	// passes validation (host_name required) yet leaves buildMetricName's
	// tier-1 value empty, so the "IcingaAlert" default is effectively
	// dead code in the reference script, faithfully reproduced as such.
	raw := []byte(`{"notification_type":"PROBLEM","host_name":"db-01","host_display_name":""}`)
	a, err := Transform(raw, Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.MetricName == "IcingaAlert" {
		t.Fatal("MetricName should never equal the hardcoded default in practice")
	}
}

func TestTransform_ConfigOverridesBeatDefaults(t *testing.T) {
	raw := []byte(`{"notification_type":"PROBLEM","host_name":"db-01"}`)
	cfg := Config{"ENVIRONMENT": "staging", "SERVICE": "client-example"}
	a, err := Transform(raw, cfg)
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Environment != "staging" {
		t.Errorf("Environment = %q, want staging", a.Environment)
	}
	if a.Service != "client-example" {
		t.Errorf("Service = %q, want client-example", a.Service)
	}
}

func TestTransform_SourceIsAlwaysIcinga(t *testing.T) {
	a, err := Transform(serviceCheckPayload(nil), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Source != "Icinga" {
		t.Errorf("Source = %q, want Icinga", a.Source)
	}
}

func TestTransform_MissingNotificationType(t *testing.T) {
	if _, err := Transform(serviceCheckPayload(map[string]any{"notification_type": ""}), Config{}); err == nil {
		t.Fatal("Transform with missing notification_type should error")
	}
}

func TestTransform_MissingHostName(t *testing.T) {
	if _, err := Transform(serviceCheckPayload(map[string]any{"host_name": ""}), Config{}); err == nil {
		t.Fatal("Transform with missing host_name should error")
	}
}
