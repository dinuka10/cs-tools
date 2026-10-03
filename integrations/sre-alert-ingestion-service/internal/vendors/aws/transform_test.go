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

package aws

import (
	"encoding/json"
	"strings"
	"testing"
)

func snsEnvelope(message any) []byte {
	var messageStr string
	switch m := message.(type) {
	case string:
		messageStr = m
	default:
		b, _ := json.Marshal(m)
		messageStr = string(b)
	}
	body := map[string]any{
		"Type":      "Notification",
		"MessageId": "abc-123",
		"TopicArn":  "arn:aws:sns:us-east-1:123456789012:alerts",
		"Message":   messageStr,
	}
	raw, _ := json.Marshal(body)
	return raw
}

func sampleAlarm(overrides map[string]any) map[string]any {
	alarm := map[string]any{
		"AlarmName":        "HighCPUAlarm",
		"AlarmArn":         "arn:aws:cloudwatch:us-east-1:123456789012:alarm:HighCPUAlarm",
		"NewStateValue":    "ALARM",
		"AlarmDescription": `{"service":"client-example-alert-integration","category":"service_interruption","environment":"production","severity":"critical"}`,
	}
	for k, v := range overrides {
		alarm[k] = v
	}
	return alarm
}

func TestTransform_BasicFieldsFromAlarmDescription(t *testing.T) {
	a, err := Transform(snsEnvelope(sampleAlarm(nil)), Config{})
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
	if a.Severity != "critical" {
		t.Errorf("Severity = %q, want critical", a.Severity)
	}
	if a.MetricName != "HighCPUAlarm" {
		t.Errorf("MetricName = %q, want HighCPUAlarm (AlarmName)", a.MetricName)
	}
	if a.UniqueIdentifier != "arn:aws:cloudwatch:us-east-1:123456789012:alarm:HighCPUAlarm" {
		t.Errorf("UniqueIdentifier = %q, want the AlarmArn", a.UniqueIdentifier)
	}
	if a.Source != "AWS" {
		t.Errorf("Source = %q, want AWS", a.Source)
	}
}

func TestTransform_RecoveredAlarmForcesSeverityOK(t *testing.T) {
	a, err := Transform(snsEnvelope(sampleAlarm(map[string]any{"NewStateValue": "OK"})), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Severity != "ok" {
		t.Errorf("Severity = %q, want ok (NewStateValue OK forces it, overriding AlarmDescription)", a.Severity)
	}
}

func TestTransform_InvalidAlarmDescriptionDegradesToConfigDefaults(t *testing.T) {
	a, err := Transform(snsEnvelope(sampleAlarm(map[string]any{"AlarmDescription": "not valid json"})), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Service != "" {
		t.Errorf("Service = %q, want empty (no AlarmDescription overrides, no hardcoded service default)", a.Service)
	}
	if a.Category != "service_interruption" {
		t.Errorf("Category = %q, want service_interruption (hardcoded default)", a.Category)
	}
}

func TestTransform_MissingAlarmDescription(t *testing.T) {
	alarm := sampleAlarm(nil)
	delete(alarm, "AlarmDescription")
	a, err := Transform(snsEnvelope(alarm), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Service != "" {
		t.Errorf("Service = %q, want empty", a.Service)
	}
	if a.Environment != "Unknown" {
		t.Errorf("Environment = %q, want Unknown (hardcoded default)", a.Environment)
	}
}

func TestTransform_SNSMessageParseFailureProducesADiagnosticAlertNotAnError(t *testing.T) {
	a, err := Transform(snsEnvelope("this is not valid json"), Config{})
	if err != nil {
		t.Fatalf("Transform should not error on an unparseable SNS Message, got: %v", err)
	}
	if a.MetricName != "SNS Message Parse Error" {
		t.Errorf("MetricName = %q, want SNS Message Parse Error", a.MetricName)
	}
	if !strings.Contains(a.Description, "Raw Payload Message:") {
		t.Errorf("Description = %q, want it to start with 'Raw Payload Message:'", a.Description)
	}
	if a.Severity != "critical" {
		t.Errorf("Severity = %q, want the hardcoded default critical", a.Severity)
	}
}

func TestTransform_ConfigOverridesBeatHardcodedDefaults(t *testing.T) {
	cfg := Config{"service": "config-default-service", "environment": "staging"}
	alarm := sampleAlarm(nil)
	delete(alarm, "AlarmDescription")
	a, err := Transform(snsEnvelope(alarm), cfg)
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Service != "config-default-service" {
		t.Errorf("Service = %q, want config-default-service", a.Service)
	}
	if a.Environment != "staging" {
		t.Errorf("Environment = %q, want staging", a.Environment)
	}
}

func TestTransform_AlarmDescriptionBeatsConfig(t *testing.T) {
	cfg := Config{"service": "config-service"}
	a, err := Transform(snsEnvelope(sampleAlarm(nil)), cfg)
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Service != "client-example-alert-integration" {
		t.Errorf("Service = %q, want the AlarmDescription value to beat config", a.Service)
	}
}

func TestTransform_DescriptionIsPrettyPrintedInnerMessage(t *testing.T) {
	a, err := Transform(snsEnvelope(sampleAlarm(nil)), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if !strings.Contains(a.Description, "\n") {
		t.Error("Description should be pretty-printed (indented, multi-line), not compact")
	}
	if !strings.Contains(a.Description, "HighCPUAlarm") {
		t.Error("Description should contain the inner alarm JSON, not the outer SNS envelope")
	}
}

func TestTransform_MissingBody(t *testing.T) {
	if _, err := Transform([]byte(""), Config{}); err == nil {
		t.Fatal("Transform(\"\") should error with a missing-body error")
	}
	if _, err := Transform([]byte("not json at all"), Config{}); err == nil {
		t.Fatal("Transform with an unparseable outer envelope should error")
	}
}

func TestTransform_MetricNameDefaultWhenNoAlarmName(t *testing.T) {
	alarm := sampleAlarm(nil)
	delete(alarm, "AlarmName")
	a, err := Transform(snsEnvelope(alarm), Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.MetricName != "Unknown Metric" {
		t.Errorf("MetricName = %q, want Unknown Metric (hardcoded default)", a.MetricName)
	}
}
