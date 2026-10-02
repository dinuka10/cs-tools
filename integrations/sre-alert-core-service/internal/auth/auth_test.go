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

package auth

import (
	"encoding/base64"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"
)

func TestVerifySecret(t *testing.T) {
	salt, err := GenerateSalt()
	if err != nil {
		t.Fatalf("GenerateSalt: %v", err)
	}
	const secret = "correct-horse-battery-staple"
	saltB64 := base64.StdEncoding.EncodeToString(salt)
	hashB64 := base64.StdEncoding.EncodeToString(HashSecret(secret, salt, Iterations))

	if !VerifySecret(secret, saltB64, hashB64, Iterations) {
		t.Error("the correct secret must verify")
	}
	if VerifySecret("wrong-secret", saltB64, hashB64, Iterations) {
		t.Error("a wrong secret must not verify")
	}
	if VerifySecret(secret[:5], saltB64, hashB64, Iterations) {
		t.Error("a prefix of the secret must not verify")
	}
	// A different iteration count derives a different key, so it must not verify.
	if VerifySecret(secret, saltB64, hashB64, Iterations+1) {
		t.Error("a mismatched iteration count must not verify")
	}
	for name, bad := range map[string][2]string{
		"malformed salt": {"not-base64!!", hashB64},
		"malformed hash": {saltB64, "not-base64!!"},
	} {
		if VerifySecret(secret, bad[0], bad[1], Iterations) {
			t.Errorf("%s must not verify", name)
		}
	}
}

func TestGenerateSalt_IsRandomAndCorrectLength(t *testing.T) {
	a, err := GenerateSalt()
	if err != nil {
		t.Fatalf("GenerateSalt: %v", err)
	}
	b, err := GenerateSalt()
	if err != nil {
		t.Fatalf("GenerateSalt: %v", err)
	}
	if len(a) != SaltLen {
		t.Errorf("salt length = %d, want %d", len(a), SaltLen)
	}
	if string(a) == string(b) {
		t.Error("two salts must not be identical")
	}
}

func TestIsExpired(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	cases := map[string]struct {
		expiresAt time.Time
		want      bool
	}{
		"unset (zero time) never expires": {time.Time{}, false},
		"future expiry is not expired":    {now.Add(time.Hour), false},
		"past expiry is expired":          {now.Add(-time.Hour), true},
		// Cosmos DB returns an unset expires_at as the epoch; without this, every
		// user provisioned without -ttl is rejected as expired.
		"cosmos epoch reads as unset": {time.Unix(0, 0), false},
		"just after epoch is expiry":  {time.Unix(1, 0), true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			u := User{ExpiresAt: tc.expiresAt}
			if got := u.IsExpired(now); got != tc.want {
				t.Errorf("IsExpired = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseCredentials(t *testing.T) {
	t.Run("bearer base64 pair", func(t *testing.T) {
		token := base64.StdEncoding.EncodeToString([]byte("alice:s3cr3t"))
		r := httptest.NewRequest("POST", "/alertz", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		u, s, ok := parseCredentials(r)
		if !ok || u != "alice" || s != "s3cr3t" {
			t.Errorf("got (%q, %q, %v)", u, s, ok)
		}
	})

	t.Run("basic auth", func(t *testing.T) {
		r := httptest.NewRequest("POST", "/alertz", nil)
		r.SetBasicAuth("alice", "s3cr3t")
		u, s, ok := parseCredentials(r)
		if !ok || u != "alice" || s != "s3cr3t" {
			t.Errorf("got (%q, %q, %v)", u, s, ok)
		}
	})

	// RFC 7235 makes the scheme case-insensitive, so a proxy normalising header casing must not turn a valid credential into a 401.
	t.Run("scheme is case-insensitive", func(t *testing.T) {
		token := base64.StdEncoding.EncodeToString([]byte("alice:s3cr3t"))
		for _, scheme := range []string{"Bearer ", "bearer ", "BEARER ", "BeArEr "} {
			r := httptest.NewRequest("POST", "/alertz", nil)
			r.Header.Set("Authorization", scheme+token)
			u, s, ok := parseCredentials(r)
			if !ok || u != "alice" || s != "s3cr3t" {
				t.Errorf("%q: got (%q, %q, %v)", scheme, u, s, ok)
			}
		}
	})

	// A secret may contain colons; only the first one separates the pair.
	t.Run("secret containing colons", func(t *testing.T) {
		token := base64.StdEncoding.EncodeToString([]byte("alice:a:b:c"))
		r := httptest.NewRequest("POST", "/alertz", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		u, s, ok := parseCredentials(r)
		if !ok || u != "alice" || s != "a:b:c" {
			t.Errorf("got (%q, %q, %v)", u, s, ok)
		}
	})

	t.Run("malformed", func(t *testing.T) {
		for _, header := range []string{
			"",
			"Bearer not-base64!!",
			"Bearer " + base64.StdEncoding.EncodeToString([]byte("no-colon")),
			"Bearer " + base64.StdEncoding.EncodeToString([]byte(":empty-username")),
			"Bearer " + base64.StdEncoding.EncodeToString([]byte("empty-secret:")),
			"Bogus scheme",
		} {
			r := httptest.NewRequest("POST", "/alertz", nil)
			if header != "" {
				r.Header.Set("Authorization", header)
			}
			if _, _, ok := parseCredentials(r); ok {
				t.Errorf("%q should not parse", header)
			}
		}
	})
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
