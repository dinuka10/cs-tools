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
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gocql/gocql"
	"github.com/scylladb/gocqlx/v2"
	"github.com/scylladb/gocqlx/v2/qb"
)

// ErrUserNotFound marks a lookup for a username with no row in integration_users.
var ErrUserNotFound = errors.New("internal user not found")

var userColumns = []string{
	"username", "id", "secret_hash", "salt", "iterations", "enabled",
	"created_at", "created_by", "updated_at", "secret_rotated_at", "last_used_at", "expires_at",
}

// UserRepo owns the integration_users table.
type UserRepo struct {
	session gocqlx.Session
}

// NewUserRepo wraps session for internal-user reads and provisioning.
func NewUserRepo(session *gocql.Session) *UserRepo {
	return &UserRepo{session: gocqlx.NewSession(session)}
}

// Get reads a user by username; returns ErrUserNotFound if no row exists.
func (r *UserRepo) Get(ctx context.Context, username string) (User, error) {
	stmt, names := qb.Select("integration_users").Columns(userColumns...).Where(qb.Eq("username")).ToCql()
	var u User
	if err := r.session.Query(stmt, names).WithContext(ctx).BindMap(qb.M{"username": username}).GetRelease(&u); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return User{}, fmt.Errorf("read internal user %s: %w", username, ErrUserNotFound)
		}
		return User{}, fmt.Errorf("read internal user %s: %w", username, err)
	}
	return u, nil
}

// Upsert inserts or replaces a user row; used by the createuser CLI tool to provision accounts.
func (r *UserRepo) Upsert(ctx context.Context, u User) error {
	stmt, names := qb.Insert("integration_users").Columns(userColumns...).ToCql()
	if err := r.session.Query(stmt, names).WithContext(ctx).BindStruct(u).ExecRelease(); err != nil {
		return fmt.Errorf("upsert internal user %s: %w", u.Username, err)
	}
	return nil
}

// List reads every row in integration_users; the table is small (service accounts only), so a full scan is fine.
func (r *UserRepo) List(ctx context.Context) ([]User, error) {
	stmt, names := qb.Select("integration_users").Columns(userColumns...).ToCql()
	var users []User
	if err := r.session.Query(stmt, names).WithContext(ctx).SelectRelease(&users); err != nil {
		return nil, fmt.Errorf("list internal users: %w", err)
	}
	return users, nil
}

// SetEnabled flips a user's enabled flag and bumps updated_at; used to disable/re-enable an account without deleting its row.
func (r *UserRepo) SetEnabled(ctx context.Context, username string, enabled bool) error {
	stmt, names := qb.Update("integration_users").Set("enabled", "updated_at").Where(qb.Eq("username")).ToCql()
	if err := r.session.Query(stmt, names).WithContext(ctx).BindMap(qb.M{"username": username, "enabled": enabled, "updated_at": time.Now().UTC()}).ExecRelease(); err != nil {
		return fmt.Errorf("set enabled=%t for internal user %s: %w", enabled, username, err)
	}
	return nil
}
