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
// Package email sends mail through WSO2's email notification service (POST /send-email),
// authenticating with OAuth2 client credentials. Same wire format as the other cs-tools clients.
package email

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/caarlos0/env/v11"
)

// Config is read from EMAIL_* env vars. An empty BaseURL disables email.
type Config struct {
	BaseURL      string `env:"EMAIL_BASE_URL"`
	TokenURL     string `env:"EMAIL_TOKEN_URL"`
	ClientID     string `env:"EMAIL_CLIENT_ID"`
	ClientSecret string `env:"EMAIL_CLIENT_SECRET"`
	FromAddress  string `env:"EMAIL_FROM_ADDRESS"`
}

// ConfigFromEnv reads Config from the environment.
func ConfigFromEnv() (Config, error) {
	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		return Config{}, fmt.Errorf("email config: %w", err)
	}
	return cfg, nil
}

// Enabled reports whether email is configured.
func (c Config) Enabled() bool { return c.BaseURL != "" }

// Client sends email. Safe for concurrent use.
type Client struct {
	cfg  Config
	http *http.Client

	mu      sync.Mutex
	token   string
	expires time.Time
}

// New validates cfg; BaseURL and TokenURL must be https (plain http only for loopback, in tests).
func New(cfg Config, timeout time.Duration) (*Client, error) {
	for name, u := range map[string]string{"EMAIL_BASE_URL": cfg.BaseURL, "EMAIL_TOKEN_URL": cfg.TokenURL} {
		if err := requireHTTPS(name, u); err != nil {
			return nil, err
		}
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" || cfg.FromAddress == "" {
		return nil, errors.New("email config: EMAIL_CLIENT_ID, EMAIL_CLIENT_SECRET and EMAIL_FROM_ADDRESS are required")
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	return &Client{cfg: cfg, http: &http.Client{Timeout: timeout, CheckRedirect: noRedirects}}, nil
}

// noRedirects stops the client following any redirect: a redirect could replay the client
// secret or bearer token to another origin, or downgrade it to plain http.
func noRedirects(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func requireHTTPS(name, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("email config: %s must be a URL", name)
	}
	if u.Scheme == "https" {
		return nil
	}
	if ip := net.ParseIP(u.Hostname()); u.Scheme == "http" && (u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())) {
		return nil
	}
	return fmt.Errorf("email config: %s must be https", name)
}

// sendRequest is POST /send-email's body; Template is the HTML body, base64-encoded as the
// service expects.
type sendRequest struct {
	To       []string `json:"to"`
	From     string   `json:"from"`
	Subject  string   `json:"subject"`
	Template []byte   `json:"template"`
}

// Send emails an HTML body to the recipients.
func (c *Client) Send(ctx context.Context, to []string, subject, html string) error {
	token, err := c.accessToken(ctx)
	if err != nil {
		return err
	}
	body, err := json.Marshal(sendRequest{To: to, From: c.cfg.FromAddress, Subject: subject, Template: []byte(html)})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+"/send-email", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("send-email: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("send-email: %d %s", resp.StatusCode, excerpt)
	}
	return nil
}

// accessToken returns a cached client-credentials token, fetching a new one near expiry.
func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.expires) {
		return c.token, nil
	}
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {c.cfg.ClientID}, "client_secret": {c.cfg.ClientSecret}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("email token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("email token: %d", resp.StatusCode)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil || tok.AccessToken == "" {
		return "", fmt.Errorf("email token: no access_token in response")
	}
	c.token = tok.AccessToken
	c.expires = time.Now().Add(time.Duration(tok.ExpiresIn)*time.Second - 30*time.Second)
	return c.token, nil
}
