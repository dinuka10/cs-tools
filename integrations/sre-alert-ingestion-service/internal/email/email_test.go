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
package email

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestSend_TokenAndRequest(t *testing.T) {
	var tokens atomic.Int32
	var got sendRequest
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			tokens.Add(1)
			_ = r.ParseForm()
			if r.Form.Get("grant_type") != "client_credentials" || r.Form.Get("client_id") != "id" || r.Form.Get("client_secret") != "secret" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":3600}`))
		case "/send-email":
			auth = r.Header.Get("Authorization")
			_ = json.NewDecoder(r.Body).Decode(&got)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c, err := New(Config{BaseURL: srv.URL, TokenURL: srv.URL + "/token", ClientID: "id", ClientSecret: "secret",
		FromAddress: "no-reply@example.com"}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := c.Send(t.Context(), []string{"team@example.com"}, "Subject", "<p>hi</p>"); err != nil {
			t.Fatal(err)
		}
	}
	if tokens.Load() != 1 {
		t.Errorf("token fetched %d times, want 1 (cached)", tokens.Load())
	}
	if auth != "Bearer tok" || got.From != "no-reply@example.com" || got.Subject != "Subject" ||
		string(got.Template) != "<p>hi</p>" || len(got.To) != 1 || got.To[0] != "team@example.com" {
		t.Errorf("auth = %q, request = %+v", auth, got)
	}
}

func TestNew_RequiresHTTPSAndFields(t *testing.T) {
	ok := Config{BaseURL: "https://mail.example.com", TokenURL: "https://idp.example.com/token", ClientID: "id", ClientSecret: "s", FromAddress: "a@b"}
	if _, err := New(ok, time.Second); err != nil {
		t.Fatalf("valid config: %v", err)
	}
	bad := ok
	bad.BaseURL = "http://mail.example.com"
	if _, err := New(bad, time.Second); err == nil {
		t.Error("plain http to a remote host must be rejected")
	}
	bad = ok
	bad.ClientSecret = ""
	if _, err := New(bad, time.Second); err == nil {
		t.Error("missing client secret must be rejected")
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { elsewhere.Add(1) }))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusTemporaryRedirect) // 307 would replay the body
	}))
	defer srv.Close()

	c, err := New(Config{BaseURL: srv.URL, TokenURL: srv.URL + "/token", ClientID: "id", ClientSecret: "secret",
		FromAddress: "a@b"}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Send(t.Context(), []string{"x@y"}, "s", "b"); err == nil {
		t.Error("a redirected token request must fail")
	}
	if elsewhere.Load() != 0 {
		t.Error("the redirect target must receive no request (it would get the client secret)")
	}
}
