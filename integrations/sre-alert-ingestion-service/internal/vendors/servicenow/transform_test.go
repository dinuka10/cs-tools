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

package servicenow

import (
	"errors"
	"testing"

	"sre-alert-ingestion-service/internal/model"
)

func TestTransform_Object(t *testing.T) {
	raw := []byte(`{"service":"svc","metric_name":"HighCPU","severity":"Critical","category":"cat",
		"environment":"production","source":"AWS","unique_identifier":"arn:aws:cloudwatch:1","description":"CPU high"}`)
	got, err := Transform(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := model.Alert{Service: "svc", MetricName: "HighCPU", Severity: "Critical", Category: "cat",
		Environment: "production", Source: "AWS", UniqueIdentifier: "arn:aws:cloudwatch:1", Description: "CPU high"}
	if len(got) != 1 || got[0] != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestTransform_Array(t *testing.T) {
	got, err := Transform([]byte(` [{"source":"AWS","severity":"Critical"},{"source":"Azure","severity":"OK"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Source != "AWS" || got[1].Severity != "OK" {
		t.Errorf("got %+v", got)
	}
}

func TestTransform_NumbersAndObjectDescription(t *testing.T) {
	got, err := Transform([]byte(`{"unique_identifier":7010000000000000123,"description":{"a":1,"b":[true]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if got[0].UniqueIdentifier != "7010000000000000123" {
		t.Errorf("unique_identifier = %q", got[0].UniqueIdentifier)
	}
	if got[0].Description != `{"a":1,"b":[true]}` {
		t.Errorf("description = %q", got[0].Description)
	}
}

func TestTransform_Rejects(t *testing.T) {
	for _, body := range []string{``, `null`, `"text"`, `42`, `{}`, `[]`, `[{}]`, `[null]`, `[1]`,
		`{"foo":"bar"}`, `{"source":"AWS"`, `[{"source":"AWS"},{"foo":1}]`} {
		if _, err := Transform([]byte(body)); !errors.Is(err, ErrNoAlert) {
			t.Errorf("%q: err = %v, want ErrNoAlert", body, err)
		}
	}
}
