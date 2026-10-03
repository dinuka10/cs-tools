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

package datadog

import (
	"encoding/json"
	"strings"
	"testing"
)

func samplePayload(fields map[string]string) []byte {
	body, _ := json.Marshal(fields)
	return body
}

func baseFields(overrides map[string]string) map[string]string {
	fields := map[string]string{
		"event_name":   "High CPU Alert",
		"trigger_name": "avg(last_5m):avg:system.cpu.user{*} > 90",
		"transition":   "Triggered",
		"alert_id":     "12345678",
		"service":      "client-alert-integration",
		"category":     "service_interruption",
		"tags":         "env:production,severity:1",
	}
	for k, v := range overrides {
		fields[k] = v
	}
	return fields
}

func TestTransform_SeverityFromTags(t *testing.T) {
	cases := []struct {
		sev  string
		want string
	}{
		{"1", "Critical"}, {"2", "Major"}, {"3", "Minor"}, {"4", "Warning"}, {"5", "OK"},
	}
	for _, tc := range cases {
		a, err := Transform(samplePayload(baseFields(map[string]string{"tags": "env:production,severity:" + tc.sev})), Config{})
		if err != nil {
			t.Fatalf("Transform: %v", err)
		}
		if a.Severity != tc.want {
			t.Errorf("severity %s -> %q, want %q", tc.sev, a.Severity, tc.want)
		}
	}
}

func TestTransform_SeverityFromPayloadFieldBeatsTag(t *testing.T) {
	a, err := Transform(samplePayload(baseFields(map[string]string{"severity": "2", "tags": "env:production,severity:5"})), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Severity != "Major" {
		t.Errorf("Severity = %q, want Major (top-level payload field should win over tag)", a.Severity)
	}
}

func TestTransform_RecoveredForcesOK(t *testing.T) {
	a, err := Transform(samplePayload(baseFields(map[string]string{"transition": "Recovered"})), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Severity != "OK" {
		t.Errorf("Severity = %q, want OK for a Recovered transition", a.Severity)
	}
}

func TestTransform_NoDataForcesCritical(t *testing.T) {
	a, err := Transform(samplePayload(baseFields(map[string]string{"transition": "No Data", "tags": "env:production,severity:5"})), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Severity != "Critical" {
		t.Errorf("Severity = %q, want Critical for a No Data transition regardless of severity tag", a.Severity)
	}
}

func TestTransform_ExplicitStateOverridesTransition(t *testing.T) {
	a, err := Transform(samplePayload(baseFields(map[string]string{"state": "COMPLETED", "transition": "Triggered"})), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Severity != "OK" {
		t.Errorf("Severity = %q, want OK when an explicit state=COMPLETED overrides the transition", a.Severity)
	}
}

func TestTransform_EnvironmentFromPayloadField(t *testing.T) {
	a, err := Transform(samplePayload(baseFields(map[string]string{"environment": "staging", "tags": "env:production,severity:1"})), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Environment != "staging" {
		t.Errorf("Environment = %q, want staging (top-level payload field should win over tag)", a.Environment)
	}
}

func TestTransform_EnvironmentFromTags(t *testing.T) {
	a, err := Transform(samplePayload(baseFields(map[string]string{"tags": "env:staging,severity:2"})), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Environment != "staging" {
		t.Errorf("Environment = %q, want staging", a.Environment)
	}
}

func TestTransform_TagAliasesResolve(t *testing.T) {
	a, err := Transform(samplePayload(baseFields(map[string]string{
		"service":  "",
		"category": "",
		"tags":     "svc:client-example,sev:2,environment:staging",
	})), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Service != "client-example" {
		t.Errorf("Service = %q, want client-example (svc: alias)", a.Service)
	}
	if a.Severity != "Major" {
		t.Errorf("Severity = %q, want Major (sev: alias)", a.Severity)
	}
	if a.Environment != "staging" {
		t.Errorf("Environment = %q, want staging (environment: alias)", a.Environment)
	}
}

// The metric/metric_name tag only wins when the built name (from
// event_name/trigger_name) is also empty -- ServiceNow's tier 1 is
// `rawBody.metric_name || buildName`, so a non-empty built name always
// beats the tag, exactly like TestTransform_MetricNameJoinsEventAndTriggerNames
// demonstrates the opposite case.
func TestTransform_MetricTagWinsWhenNoEventOrTriggerName(t *testing.T) {
	fields := map[string]string{
		"event_id": "12345678",
		"alert_id": "12345678",
		"tags":     "metric:Custom Metric",
	}
	a, err := Transform(samplePayload(fields), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.MetricName != "Custom Metric" {
		t.Errorf("MetricName = %q, want Custom Metric (metric: alias)", a.MetricName)
	}
}

func TestTransform_BareTagIgnored(t *testing.T) {
	a, err := Transform(samplePayload(baseFields(map[string]string{"tags": "env:production,severity:1,bare-tag"})), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Environment != "production" {
		t.Errorf("Environment = %q, a bare tag should not have broken parsing", a.Environment)
	}
}

func TestTransform_UniqueIdentifierIsAlertID(t *testing.T) {
	a, err := Transform(samplePayload(baseFields(map[string]string{"tags": "severity:1"})), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.UniqueIdentifier != "12345678" {
		t.Errorf("UniqueIdentifier = %q, want 12345678", a.UniqueIdentifier)
	}
}

func TestTransform_ExplicitUniqueIdentifierWinsOverAlertID(t *testing.T) {
	a, err := Transform(samplePayload(baseFields(map[string]string{"unique_identifier": "occurrence-42"})), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.UniqueIdentifier != "occurrence-42" {
		t.Errorf("UniqueIdentifier = %q, want occurrence-42", a.UniqueIdentifier)
	}
}

func TestTransform_InvalidStructure(t *testing.T) {
	if _, err := Transform([]byte(`{}`), Config{}); err == nil {
		t.Fatal("Transform({}) should error on missing event/monitor id and event/monitor name")
	}
}

func TestTransform_MonitorFieldsAcceptedAsLegacyAlias(t *testing.T) {
	fields := baseFields(nil)
	delete(fields, "event_name")
	fields["monitor_name"] = "Legacy Monitor Name"
	fields["monitor_id"] = "99999"

	a, err := Transform(samplePayload(fields), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if !strings.Contains(a.MetricName, "Legacy Monitor Name") {
		t.Errorf("MetricName = %q, want it to contain the legacy monitor_name", a.MetricName)
	}
}

func TestTransform_MetricNameJoinsEventAndTriggerNames(t *testing.T) {
	a, err := Transform(samplePayload(baseFields(nil)), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	want := "Datadog Alert: High CPU Alert / avg(last_5m):avg:system.cpu.user{*} > 90"
	if a.MetricName != want {
		t.Errorf("MetricName = %q, want %q", a.MetricName, want)
	}
}

func TestTransform_MetricNameFromPayloadFieldBeatsBuiltName(t *testing.T) {
	a, err := Transform(samplePayload(baseFields(map[string]string{"metric_name": "Custom Metric Name"})), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.MetricName != "Custom Metric Name" {
		t.Errorf("MetricName = %q, want Custom Metric Name", a.MetricName)
	}
}
