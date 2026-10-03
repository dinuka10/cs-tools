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

package elasticsearch

import (
	"errors"
	"testing"
)

func TestTransform_CanonicalExample(t *testing.T) {
	raw := []byte(`{
		"rule_id": "",
		"rule_name": "Global-Error-Threshold (Elasticsearch)",
		"state": "ACTIVE",
		"alert_id": "ZQ79cZ0B6qTDiYX-WKue",
		"severity": "1",
		"service": "client-example-alert-integration",
		"environment": "production",
		"category": "service_interruption",
		"source": "ElasticSearch"
	}`)

	got, err := Transform(raw, Config{})
	if err != nil {
		t.Fatalf("Transform returned error: %v", err)
	}

	want := Alert{
		Service:          "client-example-alert-integration",
		MetricName:       "ElasticSearch Alert: Global-Error-Threshold (Elasticsearch)",
		Severity:         "Critical",
		Category:         "service_interruption",
		Environment:      "production", // Tier-3 default; environment is never read from the payload
		Source:           "ElasticSearch",
		UniqueIdentifier: "ZQ79cZ0B6qTDiYX-WKue",
	}
	if got.Service != want.Service ||
		got.MetricName != want.MetricName ||
		got.Severity != want.Severity ||
		got.Category != want.Category ||
		got.Environment != want.Environment ||
		got.Source != want.Source ||
		got.UniqueIdentifier != want.UniqueIdentifier {
		t.Errorf("canonical mismatch\n got: %+v\nwant: %+v", got, want)
	}
	if got.Description == "" {
		t.Error("description should carry the full raw payload, got empty")
	}
}

func TestTransform_StateOverridesSeverity(t *testing.T) {
	cases := map[string]string{
		"COMPLETED": "OK",
		"ERROR":     "Critical",
	}
	for state, want := range cases {
		raw := []byte(`{"rule_name":"r","state":"` + state + `","severity":"3"}`)
		got, err := Transform(raw, Config{})
		if err != nil {
			t.Fatalf("state %s: %v", state, err)
		}
		if got.Severity != want {
			t.Errorf("state %s: severity = %q, want %q", state, got.Severity, want)
		}
	}
}

func TestTransform_ConfigOverridesDefault(t *testing.T) {
	raw := []byte(`{"rule_name":"r","state":"ACTIVE","severity":"2"}`)
	got, err := Transform(raw, Config{"ENVIRONMENT": "staging"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Environment != "staging" {
		t.Errorf("environment = %q, want staging (Tier-2 config)", got.Environment)
	}
	if got.Severity != "Major" {
		t.Errorf("severity = %q, want Major", got.Severity)
	}
	if got.Service != "managed_services" {
		t.Errorf("service = %q, want managed_services (Tier-3 default)", got.Service)
	}
}

func TestTransform_InvalidStructure(t *testing.T) {
	_, err := Transform([]byte(`{"foo":"bar"}`), Config{})
	if !errors.Is(err, ErrInvalidStructure) {
		t.Errorf("expected ErrInvalidStructure, got %v", err)
	}
}
