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

// Package risk is a MySQL-backed client for SupportPortalLite's
// customer-health/risk tracking: four tables (project_risk,
// project_health_status, risk_action_item, action_item_comment) recording a
// project's health review state, its open/closed risk history, and the
// action items and comments attached to each risk. Ported from the
// Ballerina backend's modules/risk package (client.bal, risk.bal,
// types.bal), which used ballerinax/mysql; this package uses
// database/sql + github.com/go-sql-driver/mysql, this monorepo's first
// MySQL consumer.
package risk

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/go-sql-driver/mysql" // MySQL driver, registered via side effect
)

// Connection pool bounds for the risk MySQL database. Without these,
// database/sql's defaults are unlimited open connections and connections
// that are never recycled, so a burst of requests can open far more
// connections than the MySQL server allows and a connection can go stale
// (e.g. outlive a load balancer's idle timeout) without ever being
// refreshed. No env var for these: this is a bound on the pool's own
// resource use, not a per-deployment tunable like the DSN itself.
const (
	riskMaxOpenConns    = 25
	riskMaxIdleConns    = 5
	riskConnMaxLifetime = 5 * time.Minute
)

// Config holds the configuration for the risk MySQL client.
type Config struct {
	// DSN is a github.com/go-sql-driver/mysql data source name, e.g.
	// "user:password@tcp(host:3306)/dbname?parseTime=true". parseTime=true
	// is required — this package scans DATETIME/DATE columns directly into
	// time.Time.
	DSN string
}

// Client wraps a MySQL connection pool for the risk-tracking tables.
type Client struct {
	db *sql.DB
}

// NewClient opens a connection pool to the risk-tracking MySQL database and
// verifies it is reachable with a Ping. Unlike this codebase's other
// NewXClient constructors (which build lazily and never fail), a database
// connection can genuinely be unreachable or misconfigured at startup, so
// this one returns an error — callers should treat that as fatal
// configuration, the same way this backend already treats a bad
// DASHBOARDS_DIR or CSM_TEAM_REGISTRY as fatal at startup.
func NewClient(ctx context.Context, cfg Config) (*Client, error) {
	db, err := sql.Open("mysql", cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("risk: open database: %w", err)
	}
	db.SetMaxOpenConns(riskMaxOpenConns)
	db.SetMaxIdleConns(riskMaxIdleConns)
	db.SetConnMaxLifetime(riskConnMaxLifetime)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("risk: ping database: %w", err)
	}
	return &Client{db: db}, nil
}

// Close closes the underlying connection pool.
func (c *Client) Close() error {
	return c.db.Close()
}
