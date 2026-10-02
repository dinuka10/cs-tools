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
// Package snsconfirm handles AWS SNS subscription confirmations the way the ServiceNow AWS Alert
// API did (AWSSNSNotificationUtils): confirm the subscription by fetching its SubscribeURL, then
// email the team named by the webhook's ?team= query parameter. No alert is stored. Unlike
// ServiceNow, the SNS signature is verified first; an unsigned or forged confirmation is ignored.
package snsconfirm

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// DefaultTeam is used when the webhook has no ?team=, and as the fallback address.
const DefaultTeam = "Default"

// Config mirrors ServiceNow's edge.api.aws.sns.subscription.notification.config property:
// {"teams": {"<team>": "<email>", "Default": "<email>"}}.
type Config struct {
	Teams map[string]string `json:"teams"`
}

// LoadConfig reads Config from AWS_SNS_SUBSCRIPTION_NOTIFICATION_CONFIG; unset means no emails.
func LoadConfig() (Config, error) {
	raw := strings.TrimSpace(os.Getenv("AWS_SNS_SUBSCRIPTION_NOTIFICATION_CONFIG"))
	if raw == "" {
		return Config{}, nil
	}
	var cfg Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return Config{}, fmt.Errorf("invalid AWS_SNS_SUBSCRIPTION_NOTIFICATION_CONFIG: %w", err)
	}
	return cfg, nil
}

// Mailer sends email; *email.Client implements it.
type Mailer interface {
	Send(ctx context.Context, to []string, subject, html string) error
}

// Handler confirms subscriptions and sends the notification emails.
type Handler struct {
	logger *slog.Logger
	teams  map[string]string
	mailer Mailer // nil disables email
	http   *http.Client
	// allowURL guards the confirmation fetch; only SNS's own endpoints by default.
	allowURL func(*url.URL) bool
	verifier *verifier
	sends    sync.WaitGroup
}

// New returns a Handler. mailer may be nil.
func New(logger *slog.Logger, cfg Config, mailer Mailer, timeout time.Duration) *Handler {
	h := &Handler{logger: logger, teams: cfg.Teams, mailer: mailer,
		http: &http.Client{Timeout: timeout, CheckRedirect: noRedirects}, allowURL: isSNSURL}
	h.verifier = &verifier{http: h.http, allowURL: func(u *url.URL) bool { return h.allowURL(u) }}
	return h
}

var snsHost = regexp.MustCompile(`^sns\.[a-z0-9-]+\.amazonaws\.com(\.cn)?$`)

// isSNSURL accepts only https URLs on an AWS SNS endpoint, so the service can't be made to
// fetch arbitrary addresses.
func isSNSURL(u *url.URL) bool {
	return u.Scheme == "https" && snsHost.MatchString(u.Hostname())
}

// noRedirects keeps every fetch on the SNS host that was checked.
func noRedirects(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// HandleIfConfirmation handles raw if it is an SNS SubscriptionConfirmation and reports whether
// it was one. Any other payload is left for the AWS transform.
func (h *Handler) HandleIfConfirmation(raw []byte, team string) bool {
	var msg message
	if json.Unmarshal(raw, &msg) != nil || msg.Type != "SubscriptionConfirmation" {
		return false
	}
	if team == "" {
		team = DefaultTeam
	}
	if err := h.verifier.verify(msg); err != nil {
		h.logger.Warn("SNS subscription confirmation failed signature check; ignored",
			"team", team, "topic_arn", msg.TopicArn, "error", err)
		return true
	}
	if msg.SubscribeURL == "" {
		h.logger.Error("SNS subscription confirmation has no SubscribeURL", "team", team, "topic_arn", msg.TopicArn)
		return true
	}

	if u, err := url.Parse(msg.SubscribeURL); err != nil || !h.allowURL(u) {
		h.logger.Error("SNS SubscribeURL is not an AWS SNS https URL; not fetched or emailed",
			"team", team, "topic_arn", msg.TopicArn, "subscribe_url", msg.SubscribeURL)
		return true
	}
	confirmed := h.confirm(msg.SubscribeURL)
	if !confirmed {
		h.logger.Warn("SNS subscription auto-confirm failed; sending the manual-action email", "team", team, "topic_arn", msg.TopicArn)
	}

	to := h.teams[team]
	if to == "" {
		to = h.teams[DefaultTeam]
	}
	switch {
	case to != "" && h.mailer != nil:
		h.notify(to, msg.SubscribeURL, confirmed, team)
	case !confirmed:
		h.logger.Error("CRITICAL: SNS subscription failed auto-confirm and no notification email is configured",
			"team", team, "topic_arn", msg.TopicArn, "subscribe_url", msg.SubscribeURL)
	default:
		h.logger.Warn("SNS subscription confirmed; no notification email configured", "team", team, "topic_arn", msg.TopicArn)
	}
	return true
}

func (h *Handler) confirm(subscribeURL string) bool {
	u, err := url.Parse(subscribeURL)
	if err != nil || !h.allowURL(u) {
		h.logger.Error("SNS SubscribeURL is not an AWS SNS https URL; not fetched", "subscribe_url", subscribeURL)
		return false
	}
	req, err := http.NewRequest(http.MethodGet, subscribeURL, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", "sre-alert-ingestion-service")
	resp, err := h.http.Do(req)
	if err != nil {
		h.logger.Error("SNS subscription auto-confirm request failed", "error", err)
		return false
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		h.logger.Error("SNS subscription auto-confirm rejected", "status", resp.StatusCode)
		return false
	}
	h.logger.Info("SNS subscription confirmed", "subscribe_host", u.Host)
	return true
}

// notify emails the team in the background, with ServiceNow's subject and wording.
func (h *Handler) notify(to, subscribeURL string, confirmed bool, team string) {
	subject := "AWS SNS Subscription [" + map[bool]string{true: "Auto-Confirmed", false: "ACTION REQUIRED"}[confirmed] + "]"
	status := "Confirmation Failed - Manual Action Required"
	next := "Please click the link above to confirm the subscription manually."
	if confirmed {
		status, next = "Successfully Confirmed", "No further action required."
	}
	body := "<p>AWS SNS Subscription Request.</p><p>Status: " + html.EscapeString(status) + "<br>Subscribe URL: " +
		html.EscapeString(subscribeURL) + "</p><p>" + html.EscapeString(next) + "</p>"

	h.sends.Add(1)
	go func() {
		defer h.sends.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := h.mailer.Send(ctx, []string{to}, subject, body); err != nil {
			h.logger.Error("SNS subscription email not sent", "team", team, "to", to, "confirmed", confirmed,
				"subscribe_url", subscribeURL, "error", err)
			return
		}
		h.logger.Info("SNS subscription email sent", "team", team, "to", to, "confirmed", confirmed)
	}()
}

// Wait waits for emails in flight, or ctx.
func (h *Handler) Wait(ctx context.Context) {
	done := make(chan struct{})
	go func() { h.sends.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}
