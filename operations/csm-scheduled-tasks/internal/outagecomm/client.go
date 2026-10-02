// Copyright (c) 2026 WSO2 LLC. (https://www.wso2.com).
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

// Package outagecomm is a narrow client for entity-service's outage
// COMMUNICATION sweep — the SRE-facing declaration and resolution emails.
//
// The port of ServiceNow's `Outage Communication` flow. Distinct from
// internal/outagenotify, which is the internal-STAKEHOLDER notifier: a
// different flow, a different audience, and a different idempotency
// mechanism. Two clients rather than one because the two entity-service
// endpoints are separate and will diverge.
package outagecomm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"

	"github.com/wso2-open-operations/cs-tools/operations/csm-scheduled-tasks/internal/apierror"
	"github.com/wso2-open-operations/cs-tools/operations/csm-scheduled-tasks/internal/httpsec"
)

var tokenFetchTimeout = 10 * time.Second

// sweepTimeout bounds one sweep. The candidate set is small — only outages
// opted in and not yet resolved — so a slow sweep means trouble, not volume.
const sweepTimeout = 60 * time.Second

// Config holds this client's configuration.
type Config struct {
	BaseURL      string
	TokenURL     string
	ClientID     string
	ClientSecret string
	Scopes       []string
}

// Client calls entity-service's outage communication sweep.
type Client struct {
	http    *http.Client
	baseURL string
}

// NewClient constructs a Client authenticated via the client credentials grant.
func NewClient(cfg Config) (*Client, error) {
	if err := httpsec.RequireSecureURL(cfg.TokenURL); err != nil {
		return nil, fmt.Errorf("outagecomm: token URL: %w", err)
	}
	if err := httpsec.RequireSecureURL(cfg.BaseURL); err != nil {
		return nil, fmt.Errorf("outagecomm: base URL: %w", err)
	}
	cc := clientcredentials.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		TokenURL:     cfg.TokenURL,
		Scopes:       cfg.Scopes,
	}
	tokenHTTP := &http.Client{Timeout: tokenFetchTimeout}
	httpsec.RejectInsecureRedirects(tokenHTTP)
	httpClient := cc.Client(context.WithValue(context.Background(), oauth2.HTTPClient, tokenHTTP))
	httpClient.Timeout = sweepTimeout
	httpsec.RejectInsecureRedirects(httpClient)

	return &Client{http: httpClient, baseURL: strings.TrimRight(cfg.BaseURL, "/")}, nil
}

// Decision is one outage's evaluation.
type Decision struct {
	OutageID string `json:"outageId"`
	Number   string `json:"number"`
	// Kind is DECLARED or RESOLVED. There is no update arm — the "Update"
	// rows in the communication log belong to a different flow.
	Kind    string `json:"kind"`
	Reason  string `json:"reason"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

// SweepResult is entity-service's wire shape.
type SweepResult struct {
	Evaluated int        `json:"evaluated"`
	Decisions []Decision `json:"decisions"`
}

// Sweep asks entity-service which outage communication emails are due.
//
// *** THE SWEEP RECORDS BEFORE IT RETURNS. *** Each decision is written to
// the communication log server-side before this client sees it, so calling
// Sweep twice does not yield the same email twice — and a decision this
// caller fails to deliver is LOST rather than retried.
//
// That log row is the port's whole idempotency mechanism. ServiceNow relies
// on "Run Trigger: Once" and writes no state to the outage at all, which a
// sweep cannot inherit. Recording first means a crash loses an email rather
// than repeating it, matching the internal notifier's choice for the same
// reason: a duplicate announcement to a standing group is worse than a gap.
func (c *Client) Sweep(ctx context.Context, limit int) (SweepResult, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	path := "/outage-communications/sweep"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, nil)
	if err != nil {
		return SweepResult{}, fmt.Errorf("outagecomm: build request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return SweepResult{}, fmt.Errorf("outagecomm: POST %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return SweepResult{}, fmt.Errorf("outagecomm: read response body: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return SweepResult{}, &apierror.Error{StatusCode: resp.StatusCode, Body: string(body)}
	}
	var parsed SweepResult
	if err := json.Unmarshal(body, &parsed); err != nil {
		return SweepResult{}, fmt.Errorf("outagecomm: decode response: %w", err)
	}
	return parsed, nil
}
