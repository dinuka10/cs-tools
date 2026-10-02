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

// Package model holds alert/incident shapes and severity/fingerprint normalization for the incident pipeline.
package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"math"
	"os"
	"sort"
	"strings"
	"time"
)

// Alert is the canonical shape stored by alert-ingestion-service; read and normalized here before dedup.
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

// Incident dedups alerts by fingerprint; Severity is numeric (1=Critical..5=OK); db tags drive gocqlx binding.
type Incident struct {
	Fingerprint string `json:"fingerprint" db:"fingerprint"`
	// IncidentID is CSM's UUID for PATCH; empty until confirmed. IncidentNumber is the human-readable display id.
	IncidentID     string   `json:"incident_id" db:"incident_id"`
	IncidentNumber string   `json:"incident_number" db:"incident_number"`
	Status         string   `json:"status" db:"status"`
	Severity       int      `json:"severity" db:"severity"`
	Impact         string   `json:"impact" db:"impact"`
	Urgency        string   `json:"urgency" db:"urgency"`
	Service        string   `json:"service" db:"service"`
	MetricName     string   `json:"metric_name" db:"metric_name"`
	Description    string   `json:"description" db:"description"`
	Category       string   `json:"category" db:"category"`
	Environment    string   `json:"environment" db:"environment"`
	Source         string   `json:"source" db:"source"`
	AlertIDs       []string `json:"alert_ids" db:"alert_ids"`
	AlertCount     int      `json:"alert_count" db:"alert_count"`
	WorkNotes      []string `json:"work_notes" db:"work_notes"`
	// PendingNotes is the FIFO subset not yet confirmed by CSM; kept separate because WorkNotes is tail-trimmed.
	PendingNotes []string  `json:"pending_notes" db:"pending_notes"`
	FirstSeen    time.Time `json:"first_seen" db:"first_seen"`
	LastSeen     time.Time `json:"last_seen" db:"last_seen"`
	// StateCheckedAt throttles CSM refresh calls to avoid excessive round trips during alert storms.
	StateCheckedAt time.Time `json:"state_checked_at" db:"state_checked_at"`
	// Fallback and CSMConfirmed are independent delivery obligations tracked separately.
	Fallback     bool `json:"fallback" db:"fallback"`
	CSMConfirmed bool `json:"csm_confirmed" db:"csm_confirmed"`
	// CSMAttempts and CSMPermanentlyFailed track retry limits to prevent permanently-rejected payloads from being rescanned.
	CSMAttempts          int  `json:"csm_attempts" db:"csm_attempts"`
	CSMPermanentlyFailed bool `json:"csm_permanently_failed" db:"csm_permanently_failed"`
	// CSMLastAttemptAt backs CSMRetryDue's exponential backoff, so RetrySweep doesn't hit CSM every sweep during an outage.
	CSMLastAttemptAt time.Time `json:"csm_last_attempt_at" db:"csm_last_attempt_at"`
	// Version fences mutating writes so a replica whose lease expired mid-operation can't overwrite a newer leader's update.
	Version int64 `json:"-" db:"version"`
}

// CSMRetryDue reports whether enough time has passed since the last CSM attempt, growing the wait exponentially (base, base*mult, ...) capped at maxDelay.
func (i Incident) CSMRetryDue(now time.Time, base time.Duration, multiplier float64, maxDelay time.Duration) bool {
	if i.CSMAttempts == 0 {
		return true // never attempted yet
	}
	delay := time.Duration(float64(base) * math.Pow(multiplier, float64(i.CSMAttempts-1)))
	if delay > maxDelay {
		delay = maxDelay
	}
	return now.Sub(i.CSMLastAttemptAt) >= delay
}

// IsOpen reports open status; false if permanently-failed, older than dedupWindow, or CSM-confirmed as closed.
func (i Incident) IsOpen(now time.Time, dedupWindow time.Duration) bool {
	if i.CSMPermanentlyFailed {
		return false
	}
	if now.Sub(i.FirstSeen) >= dedupWindow {
		return false
	}
	if !i.CSMConfirmed {
		return true
	}
	return !strings.EqualFold(strings.TrimSpace(i.Status), "closed")
}

// Defaults are fallback Alert field values sourced from the CORE_ALERT_DEFAULTS env var.
type Defaults struct {
	Service     string `json:"service"`
	MetricName  string `json:"metric_name"`
	Severity    string `json:"severity"`
	Category    string `json:"category"`
	Environment string `json:"environment"`
	Source      string `json:"source"`
}

// LoadDefaults fails loudly on malformed JSON rather than silently dropping configured fallbacks.
func LoadDefaults() (Defaults, error) {
	raw := os.Getenv("CORE_ALERT_DEFAULTS")
	if strings.TrimSpace(raw) == "" {
		return Defaults{}, nil
	}
	var d Defaults
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return Defaults{}, fmt.Errorf("invalid CORE_ALERT_DEFAULTS: %w", err)
	}
	return d, nil
}

// Apply fills empty Alert fields from Defaults; already-populated fields are left untouched.
func (d Defaults) Apply(a *Alert) {
	a.Service = firstNonEmpty(a.Service, d.Service)
	a.MetricName = firstNonEmpty(a.MetricName, d.MetricName)
	a.Severity = firstNonEmpty(a.Severity, d.Severity)
	a.Category = firstNonEmpty(a.Category, d.Category)
	a.Environment = firstNonEmpty(a.Environment, d.Environment)
	a.Source = firstNonEmpty(a.Source, d.Source)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// numericSeverity maps lowercase severity labels to this service's internal 0-5 scale.
var numericSeverity = map[string]int{
	"critical": 1,
	"major":    2,
	"minor":    3,
	"warning":  4,
	"ok":       5,
	"clear":    0,
}

// SeverityToNumeric defaults unrecognized labels to 1 (Critical) as fail-safe; callers should log when recognized is false.
func SeverityToNumeric(label string) (n int, recognized bool) {
	if n, ok := numericSeverity[strings.ToLower(strings.TrimSpace(label))]; ok {
		return n, true
	}
	return 1, false
}

// IsResolving reports whether severityNum is a recovery signal (OK=5 or Clear=0).
func IsResolving(severityNum int) bool {
	return severityNum == 5 || severityNum == 0
}

// ImpactUrgency maps severity to CSM's Impact/Urgency strings ("HIGH"/"MEDIUM"/"LOW"), per CreateIncidentRequest's contract.
func ImpactUrgency(severityNum int) (impact, urgency string) {
	switch severityNum {
	case 1:
		return "HIGH", "HIGH"
	case 2:
		return "MEDIUM", "HIGH"
	case 3:
		return "MEDIUM", "MEDIUM"
	case 4:
		return "MEDIUM", "LOW"
	case 5:
		return "LOW", "LOW"
	default:
		return "HIGH", "HIGH"
	}
}

// BuildWorkNote formats a work note as HTML (referencing the alert by id), since workNotes is HTML-sourced and plain "\n" would render as one unbroken line.
func BuildWorkNote(kind, alertID, metricName, source string) string {
	metricName = firstNonEmpty(metricName, "N/A")
	source = firstNonEmpty(source, "N/A")
	return fmt.Sprintf("%s alert received.<br>Alert: %s<br>Metric: %s<br>Source: %s",
		html.EscapeString(kind), html.EscapeString(alertID), html.EscapeString(metricName), html.EscapeString(source))
}

// BuildChatAnnotationText formats a Duplicate/OK annotation's body for the Chat card, omitting the alert id since the card no longer carries a header naming the incident to repeat it against.
func BuildChatAnnotationText(kind, metricName, source string) string {
	metricName = firstNonEmpty(metricName, "N/A")
	source = firstNonEmpty(source, "N/A")
	return fmt.Sprintf("<b>%s alert received.</b><br>Metric: %s<br>Source: %s",
		html.EscapeString(kind), html.EscapeString(metricName), html.EscapeString(source))
}

// kv preserves field order in HTML tables (Go map iteration is random).
type kv struct {
	key string
	val any
}

// jsonValueToHTML recursively renders JSON as HTML, matching ServiceNow's JSONToHtmlAction output.
func jsonValueToHTML(v any) string {
	switch val := v.(type) {
	case nil:
		return `<span style='color:#999; font-style:italic;'>(null)</span>`
	case []any:
		if len(val) == 0 {
			return `<span style='color:#999; font-style:italic;'>(empty list)</span>`
		}
		var b strings.Builder
		b.WriteString(`<div style='margin:5px 0; border-left:3px solid #0076a8; padding-left:10px;'>`)
		for i, item := range val {
			border := "border-bottom:1px dashed #e0e0e0;"
			if i == len(val)-1 {
				border = ""
			}
			fmt.Fprintf(&b, `<div style='padding:5px 0; %s'>%s</div>`, border, jsonValueToHTML(item))
		}
		b.WriteString(`</div>`)
		return b.String()
	case map[string]any:
		if len(val) == 0 {
			return `<span style='color:#999; font-style:italic;'>(empty object)</span>`
		}
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys) // deterministic order; the map itself has none to preserve.
		fields := make([]kv, len(keys))
		for i, k := range keys {
			fields[i] = kv{key: k, val: val[k]}
		}
		return fieldsToHTMLTable(fields)
	default:
		return html.EscapeString(fmt.Sprint(val))
	}
}

// fieldsToHTMLTable renders fields as a bordered key/value HTML table.
func fieldsToHTMLTable(fields []kv) string {
	var b strings.Builder
	b.WriteString(`<table style='border:1px solid #dcdcdc; border-collapse:collapse; width:100%; font-family:Arial, sans-serif; font-size:13px;'>`)
	for _, f := range fields {
		fmt.Fprintf(&b, `<tr><td style='background:#f5f5f5; font-weight:bold; width:30%%; padding:8px; border:1px solid #dcdcdc;'>%s</td>`+
			`<td style='padding:8px; border:1px solid #dcdcdc;'>%s</td></tr>`,
			html.EscapeString(f.key), jsonValueToHTML(f.val))
	}
	b.WriteString(`</table>`)
	return b.String()
}

// BuildCreationNote formats the initial CSM note with alert traceability and HTML table.
func BuildCreationNote(alertID string, a Alert) string {
	fields := []kv{
		{"service", a.Service},
		{"metric_name", a.MetricName},
		{"severity", a.Severity},
		{"category", a.Category},
		{"environment", a.Environment},
		{"source", a.Source},
		{"unique_identifier", a.UniqueIdentifier},
		{"description", a.Description},
	}
	return fmt.Sprintf("<p>Incident auto-created from Alert: %s</p>%s", html.EscapeString(alertID), fieldsToHTMLTable(fields))
}

// Fingerprint is the dedup key; a distinct unique identifier always starts a new incident.
func Fingerprint(source, service, metricName, environment, uniqueIdentifier string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{source, service, metricName, environment, uniqueIdentifier}, "|")))
	return hex.EncodeToString(sum[:])
}
