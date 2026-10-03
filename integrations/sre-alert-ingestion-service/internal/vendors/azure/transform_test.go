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

package azure

import (
	"encoding/json"
	"testing"
)

func sampleEnvelope(essentials, customProperties, alertContext map[string]any) []byte {
	if essentials == nil {
		essentials = map[string]any{}
	}
	body := map[string]any{
		"schemaId": "azureMonitorCommonAlertSchema",
		"data": map[string]any{
			"essentials":       essentials,
			"customProperties": customProperties,
			"alertContext":     alertContext,
		},
	}
	raw, _ := json.Marshal(body)
	return raw
}

func baseEssentials(overrides map[string]any) map[string]any {
	e := map[string]any{
		"alertId":           "/subscriptions/xxx/alerts/yyy",
		"alertRule":         "High CPU Alert",
		"severity":          "Sev0",
		"monitorCondition":  "Fired",
		"monitoringService": "Platform",
	}
	for k, v := range overrides {
		e[k] = v
	}
	return e
}

func TestTransform_SeverityMapping(t *testing.T) {
	cases := []struct {
		sev  string
		want string
	}{
		{"Sev0", "Critical"}, {"Sev1", "Major"}, {"Sev2", "Minor"}, {"Sev3", "Warning"}, {"Sev4", "OK"},
	}
	for _, tc := range cases {
		raw := sampleEnvelope(baseEssentials(map[string]any{"severity": tc.sev}), nil, nil)
		a, err := Transform(raw, Config{})
		if err != nil {
			t.Fatalf("Transform: %v", err)
		}
		if a.Severity != tc.want {
			t.Errorf("severity %s -> %q, want %q", tc.sev, a.Severity, tc.want)
		}
	}
}

func TestTransform_ResolvedForcesOK(t *testing.T) {
	raw := sampleEnvelope(baseEssentials(map[string]any{"severity": "Sev0", "monitorCondition": "Resolved"}), nil, nil)
	a, err := Transform(raw, Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Severity != "OK" {
		t.Errorf("Severity = %q, want OK when monitorCondition is Resolved", a.Severity)
	}
}

func TestTransform_MetricNameFromAlertRule(t *testing.T) {
	raw := sampleEnvelope(baseEssentials(nil), nil, nil)
	a, err := Transform(raw, Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.MetricName != "High CPU Alert" {
		t.Errorf("MetricName = %q, want High CPU Alert", a.MetricName)
	}
}

func TestTransform_CostAlertUsesBudgetName(t *testing.T) {
	essentials := baseEssentials(map[string]any{"monitoringService": "CostAlerts", "alertRule": ""})
	alertContext := map[string]any{"AlertData": map[string]any{"BudgetName": "Q3-Infra-Budget"}}
	raw := sampleEnvelope(essentials, nil, alertContext)
	a, err := Transform(raw, Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.MetricName != "Cost Alert: Q3-Infra-Budget" {
		t.Errorf("MetricName = %q, want Cost Alert: Q3-Infra-Budget", a.MetricName)
	}
}

func TestTransform_CustomPropertiesSupplyFields(t *testing.T) {
	props := map[string]any{
		"service":     "client-example-alert-integration",
		"category":    "service_interruption",
		"environment": "production",
	}
	raw := sampleEnvelope(baseEssentials(nil), props, nil)
	a, err := Transform(raw, Config{})
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

func TestTransform_DefaultsWhenNothingProvided(t *testing.T) {
	raw := sampleEnvelope(map[string]any{}, nil, nil)
	a, err := Transform(raw, Config{})
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
	if a.MetricName != "AzureCloudAlert" {
		t.Errorf("MetricName = %q, want AzureCloudAlert", a.MetricName)
	}
	// The reference script's severity default ("Critical") is only ever used
	// as a lookup key into a Sev0-Sev4 map, so it can never itself produce a
	// mapped value -- this reproduces that quirk faithfully rather than
	// smoothing it over.
	if a.Severity != "" {
		t.Errorf("Severity = %q, want empty (the Critical default isn't a valid SevN key)", a.Severity)
	}
}

func TestTransform_ConfigOverridesBeatDefaults(t *testing.T) {
	cfg := Config{"environment": "staging", "service": "client-example"}
	raw := sampleEnvelope(map[string]any{}, nil, nil)
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

func TestTransform_CustomPropertiesBeatConfig(t *testing.T) {
	cfg := Config{"service": "config-service"}
	props := map[string]any{"service": "payload-service"}
	raw := sampleEnvelope(baseEssentials(nil), props, nil)
	a, err := Transform(raw, cfg)
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Service != "payload-service" {
		t.Errorf("Service = %q, want payload-service (customProperties should beat config)", a.Service)
	}
}

func TestTransform_UniqueIdentifierIsAlertID(t *testing.T) {
	raw := sampleEnvelope(baseEssentials(nil), nil, nil)
	a, err := Transform(raw, Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.UniqueIdentifier != "/subscriptions/xxx/alerts/yyy" {
		t.Errorf("UniqueIdentifier = %q, want the alertId", a.UniqueIdentifier)
	}
}

func TestTransform_SourceIsAzure(t *testing.T) {
	raw := sampleEnvelope(baseEssentials(nil), nil, nil)
	a, err := Transform(raw, Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Source != "Azure" {
		t.Errorf("Source = %q, want Azure", a.Source)
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

func TestTransform_MissingDataDegradesGracefully(t *testing.T) {
	// No "data" object at all -- essentials/customProperties/alertContext are
	// all absent. The reference script has no structural check beyond "a
	// body was sent", so this should resolve to defaults, not error.
	raw := []byte(`{"schemaId":"azureMonitorCommonAlertSchema"}`)
	a, err := Transform(raw, Config{})
	if err != nil {
		t.Fatalf("Transform: %v", err)
	}
	if a.Service != "Managed Services" {
		t.Errorf("Service = %q, want Managed Services (defaults) when data is missing", a.Service)
	}
	if a.UniqueIdentifier != "" {
		t.Errorf("UniqueIdentifier = %q, want empty when essentials is missing", a.UniqueIdentifier)
	}
}
