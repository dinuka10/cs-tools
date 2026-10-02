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

package service

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/wso2-open-operations/cs-tools/entity-service/internal/apierror"
	"github.com/wso2-open-operations/cs-tools/entity-service/internal/auth"
	"github.com/wso2-open-operations/cs-tools/entity-service/internal/repository"
)

type fakeAccessRepo struct {
	users    []repository.AccessUser
	projects []string
	// userLookups/projectCalls count calls, so tests can prove the internal
	// path never touches the database at all.
	userLookups  int
	projectCalls int
}

func (f *fakeAccessRepo) UsersByEmail(context.Context, string) ([]repository.AccessUser, error) {
	f.userLookups++
	return f.users, nil
}
func (f *fakeAccessRepo) RegisteredProjectIDs(context.Context, string) ([]string, error) {
	f.projectCalls++
	return f.projects, nil
}

func idCtx(id auth.Identity) context.Context { return auth.WithIdentity(context.Background(), id) }

func userOf(t string, active bool) repository.AccessUser {
	return repository.AccessUser{UserType: t, Active: active}
}

var testInternalClientIDs = map[string]bool{"csm-backend": true, "integration": true}

func TestAccessService_ResolveScope(t *testing.T) {
	const email = "jane@example.com"
	tests := []struct {
		name         string
		id           auth.Identity
		users        []repository.AccessUser
		projects     []string
		wantErr      any // nil, or a pointer to the expected apierror type
		wantAll      bool
		wantProjects []string
		wantNoDBCall bool // internal-client path must never touch the repo
	}{
		{name: "identity not validated -> refuse (503), never trust the token",
			id: auth.Identity{Validated: false, UserEmail: email}, wantErr: &apierror.ServiceUnavailableError{}, wantNoDBCall: true},

		// --- Internal client: unconditional full access, user token or not ---
		{name: "internal client, no user token at all -> everything",
			id: auth.Identity{Validated: true, ClientID: "csm-backend"}, wantAll: true, wantNoDBCall: true},
		{name: "internal client forwarding a user token whose email isn't in \"user\" at all -> still everything, DB never consulted",
			id: auth.Identity{Validated: true, ClientID: "csm-backend", UserEmail: email}, users: nil, wantAll: true, wantNoDBCall: true},
		{name: "internal client forwarding a KNOWN customer's token -> still everything: internal role wins outright, no rescue/exception logic",
			id: auth.Identity{Validated: true, ClientID: "csm-backend", UserEmail: email}, users: []repository.AccessUser{userOf("EXTERNAL", true)},
			projects: []string{"p1"}, wantAll: true, wantNoDBCall: true},
		{name: "internal client forwarding an inactive user's token -> still everything",
			id: auth.Identity{Validated: true, ClientID: "csm-backend", UserEmail: email}, users: []repository.AccessUser{userOf("INTERNAL", false)}, wantAll: true, wantNoDBCall: true},

		// --- Not an internal client: resolved purely from the user token ---
		{name: "internal user sees everything",
			id: auth.Identity{Validated: true, UserEmail: email}, users: []repository.AccessUser{userOf("INTERNAL", true)}, wantAll: true},
		{name: "customer sees only registered projects",
			id: auth.Identity{Validated: true, UserEmail: email}, users: []repository.AccessUser{userOf("EXTERNAL", true)},
			projects: []string{"p1", "p2"}, wantProjects: []string{"p1", "p2"}},
		{name: "customer with no registered projects gets an EMPTY scope, not everything",
			id: auth.Identity{Validated: true, UserEmail: email}, users: []repository.AccessUser{userOf("EXTERNAL", true)},
			projects: []string{}, wantProjects: []string{}},
		{name: "email shared by an internal and an external row -> customer scope (less access)",
			id: auth.Identity{Validated: true, UserEmail: email}, users: []repository.AccessUser{userOf("INTERNAL", true), userOf("EXTERNAL", true)},
			projects: []string{"p1"}, wantProjects: []string{"p1"}},
		{name: "inactive internal row does not count",
			id: auth.Identity{Validated: true, UserEmail: email}, users: []repository.AccessUser{userOf("INTERNAL", false)}, wantErr: &apierror.ForbiddenError{}},
		{name: "inactive internal + active customer -> customer",
			id: auth.Identity{Validated: true, UserEmail: email}, users: []repository.AccessUser{userOf("INTERNAL", false), userOf("EXTERNAL", true)},
			projects: []string{"p9"}, wantProjects: []string{"p9"}},
		{name: "internal row alongside a NOT_AVAILABLE row -> denied",
			id: auth.Identity{Validated: true, UserEmail: email}, users: []repository.AccessUser{userOf("INTERNAL", true), userOf("NOT_AVAILABLE", true)}, wantErr: &apierror.ForbiddenError{}},
		{name: "system user is not a person -> denied",
			id: auth.Identity{Validated: true, UserEmail: email}, users: []repository.AccessUser{userOf("SYSTEM", true)}, wantErr: &apierror.ForbiddenError{}},
		{name: "NULL user_type -> denied",
			id: auth.Identity{Validated: true, UserEmail: email}, users: []repository.AccessUser{userOf("", true)}, wantErr: &apierror.ForbiddenError{}},
		{name: "unknown email, non-internal caller -> denied outright (no rescue -- that's internal-client only)",
			id: auth.Identity{Validated: true, UserEmail: email}, users: nil, wantErr: &apierror.ForbiddenError{}},
		{name: "unknown email forwarded by an unlisted client -> still denied",
			id: auth.Identity{Validated: true, ClientID: "stranger", UserEmail: email}, users: nil, wantErr: &apierror.ForbiddenError{}},

		{name: "no user token, unlisted client -> 401 (no legitimate caller to resolve)",
			id: auth.Identity{Validated: true, ClientID: "stranger"}, wantErr: &apierror.UnauthorizedError{}, wantNoDBCall: true},
		{name: "no user and no client at all -> 401",
			id: auth.Identity{Validated: true}, wantErr: &apierror.UnauthorizedError{}, wantNoDBCall: true},
	}
	for _, tt := range tests {
		repo := &fakeAccessRepo{users: tt.users, projects: tt.projects}
		scope, err := NewAccessService(repo, testInternalClientIDs).ResolveScope(idCtx(tt.id))

		if tt.wantNoDBCall && (repo.userLookups != 0 || repo.projectCalls != 0) {
			t.Errorf("%s: repo was consulted (userLookups=%d projectCalls=%d), want zero DB calls", tt.name, repo.userLookups, repo.projectCalls)
		}

		if tt.wantErr != nil {
			if err == nil {
				t.Errorf("%s: got scope %+v, want an error", tt.name, scope)
				continue
			}
			var ok bool
			switch tt.wantErr.(type) {
			case *apierror.ServiceUnavailableError:
				var e *apierror.ServiceUnavailableError
				ok = errors.As(err, &e)
			case *apierror.ForbiddenError:
				var e *apierror.ForbiddenError
				ok = errors.As(err, &e)
			case *apierror.UnauthorizedError:
				var e *apierror.UnauthorizedError
				ok = errors.As(err, &e)
			}
			if !ok {
				t.Errorf("%s: got %T (%v), want %T", tt.name, err, err, tt.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected error %v", tt.name, err)
			continue
		}
		if scope.Unrestricted != tt.wantAll {
			t.Errorf("%s: Unrestricted = %v, want %v", tt.name, scope.Unrestricted, tt.wantAll)
		}
		if !tt.wantAll {
			if scope.ProjectIDs == nil || len(scope.ProjectIDs) != len(tt.wantProjects) {
				t.Errorf("%s: ProjectIDs = %#v, want %v (non-nil)", tt.name, scope.ProjectIDs, tt.wantProjects)
			}
		}
	}
}

// Internal users must not trigger a project lookup at all.
func TestAccessService_InternalSkipsProjectLookup(t *testing.T) {
	repo := &fakeAccessRepo{users: []repository.AccessUser{userOf("INTERNAL", true)}}
	_, _ = NewAccessService(repo, nil).ResolveScope(idCtx(auth.Identity{Validated: true, UserEmail: "a@b.c"}))
	if repo.projectCalls != 0 {
		t.Fatalf("project lookups = %d, want 0", repo.projectCalls)
	}
}

// TestAccessService_ResolveScope_UsesCachedScopeFromContext is the
// regression test for issue #2128: callerIdentityMiddleware already calls
// ResolveScope once per request and stamps its result onto ctx for Scoped's
// benefit (repository.WithCallerIdentity) -- a second call on that same ctx
// (from case_service.go/escalation_service.go/etc, each resolving its own
// AccessScope independently) must reuse that cached value rather than
// repeating the full resolution (UsersByEmail + RegisteredProjectIDs for an
// external caller). Proven here by a fakeAccessRepo that would fail loudly
// (call counts > 0) if ResolveScope re-resolved instead of trusting the
// cache.
func TestAccessService_ResolveScope_UsesCachedScopeFromContext(t *testing.T) {
	repo := &fakeAccessRepo{}
	svc := NewAccessService(repo, testInternalClientIDs)

	want := repository.SearchScope{Unrestricted: false, ProjectIDs: []string{"p1", "p2"}, ViewerEmail: "cached@test.local"}
	// Deliberately NOT auth.WithIdentity: a cache hit must not need to
	// re-derive anything from the auth identity at all -- this ctx carries
	// only the middleware's own cached scope, exactly like a real request
	// reaching a second ResolveScope call downstream.
	ctx := repository.WithCallerIdentity(context.Background(), want)

	got, err := svc.ResolveScope(ctx)
	if err != nil {
		t.Fatalf("ResolveScope() with a cached scope on ctx: unexpected error = %v", err)
	}
	if got.Unrestricted != want.Unrestricted || got.ViewerEmail != want.ViewerEmail || !slices.Equal(got.ProjectIDs, want.ProjectIDs) {
		t.Errorf("ResolveScope() = %+v, want the cached %+v unchanged", got, want)
	}
	if repo.userLookups != 0 || repo.projectCalls != 0 {
		t.Errorf("repo calls = %d userLookups, %d projectCalls, want 0 and 0 -- a cache hit must never touch the database", repo.userLookups, repo.projectCalls)
	}
}

// TestAccessService_ResolveScope_FallsThroughWithoutCachedScope confirms the
// cache-first check in TestAccessService_ResolveScope_UsesCachedScopeFromContext
// doesn't break the ordinary (uncached) path: a ctx with no prior
// repository.WithCallerIdentity stamp -- the normal case for the middleware's
// own first call on a request -- still resolves for real, against the repo.
func TestAccessService_ResolveScope_FallsThroughWithoutCachedScope(t *testing.T) {
	repo := &fakeAccessRepo{users: []repository.AccessUser{userOf("EXTERNAL", true)}, projects: []string{"p1"}}
	svc := NewAccessService(repo, testInternalClientIDs)

	scope, err := svc.ResolveScope(idCtx(auth.Identity{Validated: true, UserEmail: "real@test.local"}))
	if err != nil {
		t.Fatalf("ResolveScope() with no cached scope: unexpected error = %v", err)
	}
	if scope.Unrestricted || len(scope.ProjectIDs) != 1 || scope.ProjectIDs[0] != "p1" {
		t.Errorf("ResolveScope() = %+v, want a real resolution against the repo (ProjectIDs=[p1])", scope)
	}
	if repo.userLookups != 1 || repo.projectCalls != 1 {
		t.Errorf("repo calls = %d userLookups, %d projectCalls, want exactly 1 and 1 -- the uncached path must still do real work", repo.userLookups, repo.projectCalls)
	}
}
