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

// Package outagenotify is a narrow client for entity-service's outage
// internal-notification sweep, plus the wire types it returns.
//
// The port of ServiceNow's `Internal Stakeholders Email Notification - Outage
// Communication`. A sweep rather than a record trigger because nothing in this
// stack writes `outage` — csm-sync-service mirrors it in from ServiceNow, so
// there is no local write to react to.
package outagenotify

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

	"github.com/wso2-open-operations/cs-tools/operations/csm-scheduled-tasks/internal/apierror"
	"github.com/wso2-open-operations/cs-tools/operations/csm-scheduled-tasks/internal/httpsec"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

var tokenFetchTimeout = 10 * time.Second

// sweepTimeout bounds one sweep. The scan is small — only outages opted into
// notification and not yet resolved — so a slow one means trouble, not volume.
const sweepTimeout = 60 * time.Second

// Config holds this client's configuration.
type Config struct {
	BaseURL      string
	TokenURL     string
	ClientID     string
	ClientSecret string
	Scopes       []string
}

// Client calls entity-service's outage notification sweep.
type Client struct {
	http    *http.Client
	baseURL string
}

// NewClient constructs a Client authenticated via the client credentials grant.
func NewClient(cfg Config) (*Client, error) {
	if err := httpsec.RequireSecureURL(cfg.TokenURL); err != nil {
		return nil, fmt.Errorf("outagenotify: token URL: %w", err)
	}
	if err := httpsec.RequireSecureURL(cfg.BaseURL); err != nil {
		return nil, fmt.Errorf("outagenotify: base URL: %w", err)
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

// Decision is one outage's evaluation: which email is due, and its rendered
// subject and body.
type Decision struct {
	OutageID string `json:"outageId"`
	Number   string `json:"number"`
	Kind     string `json:"kind"`
	Reason   string `json:"reason"`
	Subject  string `json:"subject"`
	Body     string `json:"body"`
}

// SweepResult is entity-service's wire shape.
type SweepResult struct {
	Evaluated int               `json:"evaluated"`
	Decisions []Decision        `json:"decisions"`
	Errors    map[string]string `json:"errors,omitempty"`
}

// Sweep asks entity-service which outage emails are due.
//
// *** THE SWEEP RECORDS BEFORE IT RETURNS. *** Each decision is written down
// server-side as sent before this client ever sees it, so calling Sweep twice
// does not yield the same email twice — but it also means a decision this
// caller fails to deliver is LOST rather than retried. That is deliberate: a
// duplicate outage notice to the whole internal audience is worse than a
// missed one. See entity-service's Sweep doc comment.
func (c *Client) Sweep(ctx context.Context, limit int) (SweepResult, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	path := "/outage-notifications/sweep"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, nil)
	if err != nil {
		return SweepResult{}, fmt.Errorf("outagenotify: build request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return SweepResult{}, fmt.Errorf("outagenotify: POST %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return SweepResult{}, fmt.Errorf("outagenotify: read response body: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return SweepResult{}, &apierror.Error{StatusCode: resp.StatusCode, Body: string(body)}
	}
	var parsed SweepResult
	if err := json.Unmarshal(body, &parsed); err != nil {
		return SweepResult{}, fmt.Errorf("outagenotify: decode response: %w", err)
	}
	return parsed, nil
}
