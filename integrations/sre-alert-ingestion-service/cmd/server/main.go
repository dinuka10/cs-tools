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

// Command server wires Cassandra, the allocator and vendor transforms, then serves the webhook routes.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/gocql/gocql"

	"sre-alert-ingestion-service/internal/allocator"
	"sre-alert-ingestion-service/internal/auth"
	"sre-alert-ingestion-service/internal/cassandra"
	"sre-alert-ingestion-service/internal/chat"
	"sre-alert-ingestion-service/internal/config"
	"sre-alert-ingestion-service/internal/corewake"
	"sre-alert-ingestion-service/internal/email"
	"sre-alert-ingestion-service/internal/server"
	"sre-alert-ingestion-service/internal/snsconfirm"
	"sre-alert-ingestion-service/internal/vendors"
)

// claimJitter bounds the random pause before retrying a rejected compare-and-set.
const claimJitter = 20 * time.Millisecond

// chatTimeout bounds each Google Chat post, matching sre-alert-core-service's http_timeout.
const chatTimeout = 10 * time.Second

// dbFailureInterval is the window fallback.cards_per_minute applies to.
const dbFailureInterval = time.Minute

// authCacheTTL is how long a verified credential is reused, capped at the row's expires_at.
const authCacheTTL = 60 * time.Second

// snsConfirmTimeout bounds the SubscribeURL fetch; emailTimeout bounds each email-service call.
const (
	snsConfirmTimeout = 10 * time.Second
	emailTimeout      = 15 * time.Second
)

func main() {
	base := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("app", "sre-alert-ingestion-service")
	logger := base.With("component", "main")

	cfg, err := config.Load("")
	if err != nil {
		logger.Error("failed to load deployment config", "error", err)
		os.Exit(1)
	}
	envCfg, err := config.LoadEnv()
	if err != nil {
		logger.Error("failed to read environment", "error", err)
		os.Exit(1)
	}
	registry, err := vendors.New()
	if err != nil {
		logger.Error("failed to load vendor config", "error", err)
		os.Exit(1)
	}

	cassCfg, err := cassandra.ConfigFromEnv()
	if err != nil {
		logger.Error("failed to read cassandra config", "error", err)
		os.Exit(1)
	}
	// The driver's own timeout must not cut the longer claim_timeout short.
	session, err := connectWithRetry(logger, cassCfg, cfg.Cassandra,
		max(cfg.Store.QueryTimeout.Duration(), cfg.Store.ClaimTimeout.Duration()))
	if err != nil {
		logger.Error("failed to connect to cassandra", "error", err)
		os.Exit(1)
	}
	defer session.Close()

	if cfg.LegacyAuthSection {
		logger.Warn("config.toml has an [auth] section, which is no longer read; set AUTH_ENABLED (and AUTH_AUDIT_ONLY) in the environment instead")
	}
	// After the session: AUTH_ENABLED checks webhooks against alerts-core's integration_users.
	var authn auth.Authenticator = auth.None{}
	switch {
	case envCfg.AuthEnabled && envCfg.AuthAuditOnly:
		authn = auth.NewAudit(auth.NewIntegrationUsers(session, cfg.Store.QueryTimeout.Duration(), authCacheTTL),
			base.With("component", "auth"))
		logger.Warn("AUTH_AUDIT_ONLY is set: credentials are checked but nothing is rejected")
	case envCfg.AuthEnabled:
		authn = auth.NewIntegrationUsers(session, cfg.Store.QueryTimeout.Duration(), authCacheTTL)
		logger.Info("auth enabled: vendor webhooks are checked against integration_users")
	default:
		logger.Warn("AUTH_ENABLED is not true: vendor routes are unauthenticated")
		if envCfg.AuthAuditOnly {
			logger.Warn("AUTH_AUDIT_ONLY is set but ignored, since AUTH_ENABLED is not true")
		}
	}

	store := cassandra.NewStore(session, cfg.Store.QueryTimeout.Duration(), cfg.Store.ClaimTimeout.Duration())
	if err := store.SeedSeq(context.Background()); err != nil {
		logger.Error("failed to seed alert_seq", "error", err)
		os.Exit(1)
	}

	// The replica name on cards and logs; in Choreo the hostname is the pod name.
	replica, err := os.Hostname()
	if err != nil {
		replica = "unknown"
	}
	cards := chat.New(base.With("component", "chat"), envCfg.ChatWebhookURLs, replica, chat.Settings{
		RejectWindow:     cfg.Reject.Window.Duration(),
		BodyPreviewChars: cfg.Reject.BodyPreviewChars,
		CardsPerMinute:   cfg.Fallback.CardsPerMinute,
		SummaryInterval:  dbFailureInterval,
		HTTPTimeout:      chatTimeout,
	})
	waker := corewake.New(base.With("component", "corewake"), envCfg.WakeURL, envCfg.WakeUsername, envCfg.WakeSecret, cfg.Wake.Timeout.Duration())

	sns, err := newSNSConfirmer(base.With("component", "snsconfirm"))
	if err != nil {
		logger.Error("failed to configure SNS subscription handling", "error", err)
		os.Exit(1)
	}

	alloc := allocator.New(base.With("component", "allocator"), store, cards, waker, allocator.Config{
		QueueSize:        cfg.Allocator.QueueSize,
		QueueMaxBytes:    cfg.Allocator.QueueMaxBytes,
		MaxBatch:         cfg.Allocator.MaxBatch,
		WriteConcurrency: cfg.Allocator.WriteConcurrency,
		ClaimMaxAttempts: cfg.Allocator.ClaimMaxAttempts,
		InsertAttempts:   cfg.Store.InsertAttempts,
		InsertBaseDelay:  cfg.Store.InsertBaseDelay.Duration(),
		QueryTimeout:     cfg.Store.QueryTimeout.Duration(),
		WriteDeadline:    cfg.Store.WriteDeadline.Duration(),
		ClaimJitter:      claimJitter,
	})

	srv := server.New(server.Options{
		Logger:       base.With("component", "server"),
		Auth:         authn,
		Pipeline:     server.NewIngestor(registry, alloc, cfg.Server.RequestWait.Duration()).WithSNSConfirmer(sns),
		Rejects:      cards,
		Vendors:      registry.Names(),
		MaxBodyBytes: cfg.Server.MaxBodyBytes,
		PreviewChars: cfg.Reject.BodyPreviewChars,
		ReadTimeout:  cfg.Server.ReadTimeout.Duration(),
		WriteTimeout: cfg.Server.WriteTimeout.Duration(),
		IdleTimeout:  cfg.Server.IdleTimeout.Duration(),
	})
	httpSrv := srv.HTTPServer(":" + envCfg.Port)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("server listening", "port", envCfg.Port)
		serveErr <- httpSrv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server exited unexpectedly", "error", err)
			os.Exit(1)
		}
	case <-ctx.Done():
		logger.Info("shutdown signal received, draining")
		// Restores default signal handling so a second Ctrl-C force-kills.
		stop()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownGrace.Duration())
		defer cancel()
		shutdown(shutdownCtx, logger, srv, httpSrv, alloc, budget{
			DrainDelay:     cfg.Server.DrainDelay.Duration(),
			RequestWait:    cfg.Server.RequestWait.Duration(),
			AllocatorDrain: cfg.Server.AllocatorDrain.Duration(),
		}, waker.Wait, sns.Wait, cards.Close)
	}
}

// newSNSConfirmer builds the SNS subscription handler; without EMAIL_BASE_URL it still auto-confirms.
func newSNSConfirmer(logger *slog.Logger) (*snsconfirm.Handler, error) {
	teams, err := snsconfirm.LoadConfig()
	if err != nil {
		return nil, err
	}
	emailCfg, err := email.ConfigFromEnv()
	if err != nil {
		return nil, err
	}
	var mailer snsconfirm.Mailer
	if emailCfg.Enabled() {
		client, err := email.New(emailCfg, emailTimeout)
		if err != nil {
			return nil, err
		}
		mailer = client
	} else {
		logger.Warn("EMAIL_BASE_URL not set; SNS subscription emails are disabled")
	}
	return snsconfirm.New(logger, teams, mailer, snsConfirmTimeout), nil
}

// connectWithRetry backs off exponentially so a transient startup outage doesn't crash-loop the pod.
func connectWithRetry(logger *slog.Logger, cfg cassandra.Config, ccfg config.CassandraConfig, queryTimeout time.Duration) (*gocql.Session, error) {
	var session *gocql.Session
	attempt := 0
	operation := func() error {
		attempt++
		s, err := cassandra.Connect(cfg, ccfg.ConnectTimeout.Duration(), queryTimeout)
		if err != nil {
			logger.Warn("cassandra connection failed, retrying", "attempt", attempt, "max_attempts", ccfg.ConnectMaxAttempts, "error", err)
			return err
		}
		session = s
		return nil
	}
	eb := backoff.NewExponentialBackOff()
	eb.InitialInterval = ccfg.ConnectBaseDelay.Duration()
	if err := backoff.Retry(operation, backoff.WithMaxRetries(eb, uint64(ccfg.ConnectMaxAttempts-1))); err != nil {
		return nil, err
	}
	return session, nil
}
