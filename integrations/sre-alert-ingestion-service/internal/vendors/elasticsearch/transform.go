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

// Package elasticsearch transforms an inbound Elasticsearch/Watcher alert
// payload into the canonical alert JSON that the core component accepts. It is
// a Go port of the ServiceNow ElasticSearchIncidentUtils Script Include.
package elasticsearch

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"sre-alert-ingestion-service/internal/vendors/jsonnum"
	"sre-alert-ingestion-service/internal/vendors/vendorutil"
)

// source identifies alerts produced by this adapter.
const source = "ElasticSearch"

// Alert-state values Elasticsearch sends in the "state" field.
const (
	stateCompleted = "COMPLETED"
	stateError     = "ERROR"
)

// Tier-3 hardcoded defaults, used when a field is absent from both the payload
// and the operator-supplied config. Keyed by canonical field name.
var defaults = map[string]string{
	"METRIC_NAME": "ElasticSearchAlert",
	"SERVICE":     "managed_services",
	"CATEGORY":    "service_interruption",
	"ENVIRONMENT": "production",
	"SEVERITY":    "1",
}

// numericSeverityMap maps Elasticsearch/Watcher numeric severities to the
// canonical severity labels the core component expects.
var numericSeverityMap = map[string]string{
	"1": "Critical",
	"2": "Major",
	"3": "Minor",
	"4": "Warning",
	"5": "OK",
}

// ErrInvalidStructure is returned when the payload is not a recognisable
// Elasticsearch alert (missing both rule_id and rule_name).
var ErrInvalidStructure = errors.New("INVALID ELASTICSEARCH ALERT PAYLOAD STRUCTURE")

// Alert is the canonical alert model handed to the core component. Field order
// and JSON keys match the structure the core accepts.
type Alert struct {
	Service          string `json:"service"`
	MetricName       string `json:"metric_name"`
	Severity         string `json:"severity"`
	Category         string `json:"category"`
	Environment      string `json:"environment"`
	Source           string `json:"source"`
	UniqueIdentifier string `json:"unique_identifier"`
	Description      string `json:"description"`
}

// Config holds Tier-2 operator overrides (the analog of the ServiceNow
// "edge.api.elasticsearch.alert.config" system property). Any field left empty
// falls back to the Tier-3 default. Keyed by canonical field name, e.g.
// {"ENVIRONMENT": "staging", "SERVICE": "client-example"}.
type Config map[string]string

// LoadConfig reads Tier-2 overrides from the ELASTICSEARCH_ALERT_CONFIG env var
// (a JSON object). An unset or empty var yields an empty config; malformed JSON
// returns an error so misconfiguration fails loudly at startup.
func LoadConfig() (Config, error) {
	raw := os.Getenv("ELASTICSEARCH_ALERT_CONFIG")
	if strings.TrimSpace(raw) == "" {
		return Config{}, nil
	}
	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return nil, fmt.Errorf("invalid ELASTICSEARCH_ALERT_CONFIG: %w", err)
	}
	return cfg, nil
}

// Transform parses a raw inbound Elasticsearch alert and produces the canonical
// Alert. cfg supplies Tier-2 overrides; pass an empty Config to rely on payload
// values and hardcoded defaults only.
func Transform(raw []byte, cfg Config) (Alert, error) {
	var payload map[string]any
	if err := jsonnum.Unmarshal(raw, &payload); err != nil {
		return Alert{}, fmt.Errorf("%w: %v", ErrInvalidStructure, err)
	}

	// Validation: a genuine Elasticsearch alert carries a rule identity.
	if vendorutil.Str(payload, "rule_id") == "" && vendorutil.Str(payload, "rule_name") == "" {
		return Alert{}, ErrInvalidStructure
	}

	ruleName := vendorutil.Str(payload, "rule_name")
	triggerName := vendorutil.Str(payload, "trigger_name")
	state := vendorutil.Str(payload, "state")

	alert := Alert{
		Service:          configValue(cfg, "SERVICE", vendorutil.Str(payload, "service")),
		MetricName:       configValue(cfg, "METRIC_NAME", buildMetricName(ruleName, triggerName)),
		Severity:         resolveSeverity(state, vendorutil.Str(payload, "severity"), cfg),
		Category:         configValue(cfg, "CATEGORY", vendorutil.Str(payload, "category")),
		Environment:      configValue(cfg, "ENVIRONMENT", ""), // never taken from payload
		Source:           source,
		UniqueIdentifier: vendorutil.Str(payload, "alert_id"),
		Description:      vendorutil.CompactJSON(raw),
	}
	return alert, nil
}

// resolveSeverity maps the alert state and raw severity to a canonical label.
// A COMPLETED run resolves to OK and an ERROR run to Critical regardless of the
// reported severity; otherwise the numeric severity is mapped.
func resolveSeverity(state, rawSeverity string, cfg Config) string {
	switch state {
	case stateCompleted:
		return "OK"
	case stateError:
		return "Critical"
	}
	if rawSeverity == "" {
		// Mirror the reference default chain: config SEVERITY, then Tier-3.
		rawSeverity = vendorutil.FirstNonEmpty(cfg["SEVERITY"], defaults["SEVERITY"])
	}
	if mapped, ok := numericSeverityMap[rawSeverity]; ok {
		return mapped
	}
	return rawSeverity
}

// buildMetricName joins rule and trigger names into a human-readable metric
// name, or returns "" when neither is present (letting config/defaults win).
func buildMetricName(ruleName, triggerName string) string {
	parts := make([]string, 0, 2)
	for _, p := range []string{ruleName, triggerName} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "ElasticSearch Alert: " + strings.Join(parts, " / ")
}

// configValue applies the 3-tier resolution: payload value, then operator
// config, then the hardcoded default for the field.
func configValue(cfg Config, field, payloadValue string) string {
	return vendorutil.FirstNonEmpty(payloadValue, cfg[field], defaults[field])
}
