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

package vendors

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"sre-alert-ingestion-service/internal/model"
)

var allVendors = []string{
	"aws", "azure", "datadog", "elasticsearch", "gcp",
	"icinga", "openobserve", "opensearch", "prometheus", "servicenow", "site24x7",
}

// newTestRegistry clears every <VENDOR>_ALERT_CONFIG so tests run on built-in defaults, except
// Site24x7, whose tag mapping is operator-configured only and would otherwise leave
// service/category/environment empty.
func newTestRegistry(t *testing.T) *Registry {
	t.Helper()
	for _, v := range allVendors {
		t.Setenv(strings.ToUpper(v)+"_ALERT_CONFIG", "")
	}
	t.Setenv("SITE24X7_ALERT_CONFIG", `{"TagList":{"Service":"svc","Category":"cat","Environment":"env"}}`)
	r, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

// compactFile returns a testdata payload as single-line JSON.
func compactFile(t *testing.T, vendor, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", vendor+"_"+name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func transformFile(t *testing.T, r *Registry, vendor, name string) []model.Alert {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", vendor+"_"+name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	tr, ok := r.Lookup(vendor)
	if !ok {
		t.Fatalf("vendor %s not registered", vendor)
	}
	alerts, err := tr(raw)
	if err != nil {
		t.Fatalf("%s %s: %v", vendor, name, err)
	}
	if len(alerts) != 1 {
		t.Fatalf("%s %s: got %d alerts, want 1", vendor, name, len(alerts))
	}
	return alerts
}

func TestRegistry_Names(t *testing.T) {
	if got := strings.Join(newTestRegistry(t).Names(), ","); got != strings.Join(allVendors, ",") {
		t.Errorf("Names = %s", got)
	}
}

func TestRegistry_UnknownVendor(t *testing.T) {
	if _, ok := newTestRegistry(t).Lookup("splunk"); ok {
		t.Error("splunk should not be registered")
	}
}

func TestRegistry_MalformedConfigFailsStartup(t *testing.T) {
	for _, v := range allVendors {
		t.Setenv(strings.ToUpper(v)+"_ALERT_CONFIG", "")
	}
	t.Setenv("DATADOG_ALERT_CONFIG", "{not json")
	if _, err := New(); err == nil {
		t.Error("a malformed vendor config should fail New")
	}
}

// TestRegistry_FiringAndRecoveryShareFingerprint: alerts-core fingerprints
// source|service|metric_name|environment|unique_identifier, so a firing alert and its
// recovery must agree on all five or the recovery opens a new incident instead of
// resolving the first one.
func TestRegistry_FiringAndRecoveryShareFingerprint(t *testing.T) {
	r := newTestRegistry(t)
	for _, v := range allVendors {
		t.Run(v, func(t *testing.T) {
			f := transformFile(t, r, v, "firing")[0]
			rec := transformFile(t, r, v, "recovery")[0]
			fields := []struct{ name, firing, recovery string }{
				{"source", f.Source, rec.Source},
				{"service", f.Service, rec.Service},
				{"metric_name", f.MetricName, rec.MetricName},
				{"environment", f.Environment, rec.Environment},
				{"unique_identifier", f.UniqueIdentifier, rec.UniqueIdentifier},
			}
			for _, fld := range fields {
				if fld.firing != fld.recovery {
					t.Errorf("%s differs: firing %q, recovery %q", fld.name, fld.firing, fld.recovery)
				}
			}
			// Every sample carries its vendor id, so none should fall back to the ALT id.
			if f.UniqueIdentifier == "" {
				t.Error("unique_identifier is empty; the sample carries a vendor id")
			}
		})
	}
}

// resolvesInAlertsCore mirrors sre-alert-core-service's model: only "ok" and "clear"
// (case-insensitive) resolve an incident.
func resolvesInAlertsCore(severity string) bool {
	s := strings.ToLower(strings.TrimSpace(severity))
	return s == "ok" || s == "clear"
}

// TestRegistry_RecoveryResolvesInAlertsCore records which vendors' recoveries alerts-core
// actually treats as resolving. OpenObserve's transform has no recovery state at all
// (severity comes only from urgency/impact, which map to Critical/Major/Minor), so its
// incidents never close from a recovery webhook. This is existing transform behaviour, kept
// unchanged; the test pins it so any change is visible.
func TestRegistry_RecoveryResolvesInAlertsCore(t *testing.T) {
	r := newTestRegistry(t)
	neverResolves := map[string]bool{"openobserve": true}
	for _, v := range allVendors {
		t.Run(v, func(t *testing.T) {
			f := transformFile(t, r, v, "firing")[0]
			rec := transformFile(t, r, v, "recovery")[0]
			if resolvesInAlertsCore(f.Severity) {
				t.Errorf("firing severity %q would resolve the incident", f.Severity)
			}
			if got, want := resolvesInAlertsCore(rec.Severity), !neverResolves[v]; got != want {
				t.Errorf("recovery severity %q resolves = %v, want %v", rec.Severity, got, want)
			}
		})
	}
}

// TestRegistry_OpenObserveKeepsOnlyCanonicalFields checks the mapping that drops
// OpenObserve's extra ServiceNow Incident fields.
func TestRegistry_OpenObserveKeepsOnlyCanonicalFields(t *testing.T) {
	a := transformFile(t, newTestRegistry(t), "openobserve", "firing")[0]
	if a.UniqueIdentifier != "9f3a7c2e-4b1d-4e8a-9c3f-2b8d5e6f1a9c" || a.Source != "OpenObserve" ||
		a.Description != "p99 latency has been above 2000ms for 5 minutes\n\nRaw payload: "+compactFile(t, "openobserve", "firing") {
		t.Errorf("alert = %+v", a)
	}
}

// openapiVendorPath matches the vendor paths in openapi.yaml; commented-out lines don't match.
var openapiVendorPath = regexp.MustCompile(`(?m)^  /([a-z0-9]+):[ \t]*$`)

func vendorPaths(doc string) []string {
	var paths []string
	for _, m := range openapiVendorPath.FindAllStringSubmatch(doc, -1) {
		paths = append(paths, m[1])
	}
	slices.Sort(paths)
	return paths
}

func TestOpenAPIVendorPathIgnoresComments(t *testing.T) {
	doc := "paths:\n  /aws:\n    post:\n#  /azure:\n  # /gcp:\n"
	if got := vendorPaths(doc); !slices.Equal(got, []string{"aws"}) {
		t.Errorf("matched %v, want only the active aws path", got)
	}
}

// TestOpenAPIHasAPathPerVendor keeps openapi.yaml, which Choreo uses for the endpoint's
// resources, in step with the registry: every vendor needs a path, and every path a vendor.
func TestOpenAPIHasAPathPerVendor(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got, names := vendorPaths(string(raw)), newTestRegistry(t).Names(); !slices.Equal(got, names) {
		t.Errorf("openapi.yaml vendor paths = %v, registry = %v", got, names)
	}
}
