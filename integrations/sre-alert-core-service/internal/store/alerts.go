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

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gocql/gocql"
	"github.com/scylladb/gocqlx/v2"
	"github.com/scylladb/gocqlx/v2/qb"

	"alert-core-service/internal/model"
)

// ErrMalformedAlert marks a decode failure as permanent, unlike retryable transient read errors.
var ErrMalformedAlert = errors.New("malformed alert payload")

// ErrAlertNotFound marks a row not yet visible (expected during replication lag), distinct from other read errors so callers bound only this expected case with a timeout, never a real outage.
var ErrAlertNotFound = errors.New("alert not found")

type alertRow struct {
	Payload string `db:"alert"`
}

// AlertRepo reads the alerts table; alert-ingestion writes it, alert-core-service only ever reads it.
type AlertRepo struct {
	session gocqlx.Session
}

// NewAlertRepo wraps session for alert reads; nothing to seed since alert-ingestion owns writes.
func NewAlertRepo(session *gocql.Session) *AlertRepo {
	return &AlertRepo{session: gocqlx.NewSession(session)}
}

func (r *AlertRepo) Get(ctx context.Context, id string) (model.Alert, error) {
	stmt, names := qb.Select("alerts").Columns("alert").Where(qb.Eq("id")).ToCql()
	var row alertRow
	if err := r.session.Query(stmt, names).WithContext(ctx).BindMap(qb.M{"id": id}).GetRelease(&row); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return model.Alert{}, fmt.Errorf("read alert %s: %w", id, ErrAlertNotFound)
		}
		return model.Alert{}, fmt.Errorf("read alert %s: %w", id, err)
	}
	var a model.Alert
	if err := json.Unmarshal([]byte(row.Payload), &a); err != nil {
		return model.Alert{}, fmt.Errorf("decode alert %s: %w: %v", id, ErrMalformedAlert, err)
	}
	return a, nil
}
