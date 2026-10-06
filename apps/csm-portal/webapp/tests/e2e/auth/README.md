<!--
Copyright (c) 2026 WSO2 LLC. (https://www.wso2.com).

WSO2 LLC. licenses this file to you under the Apache License,
Version 2.0 (the "License"); you may not use this file except
in compliance with the License. You may obtain a copy of the License at
http://www.apache.org/licenses/LICENSE-2.0
-->

# E2E auth — captured sessions (local)

The specs run **authenticated** by replaying a captured browser session, so no
login page or 2FA is driven locally. You capture a session once per role into
`tests/e2e/storageState/<role>.json` (git-ignored — it holds real tokens).

> **Why a full bundle, not just localStorage:** the Asgardeo React SDK keeps its
> tokens in **`sessionStorage`** (`session_data-instance_…`), with only an
> `asgardeo-session-active` flag in localStorage. Playwright's `storageState`
> restores localStorage/cookies but **not** sessionStorage, so we capture both
> stores and replay them via an init script (see `fixtures/test.ts`).

## Capture a session automatically (no human step)

`generate-session.spec.ts` drives the exact same sign-in flow a human would in
a browser — it navigates the webapp, lets `AuthGuard` redirect it to the IdP,
fills in mock-oidc's own plain "Email"/"Groups" sign-in form, and submits —
then captures the resulting localStorage + sessionStorage into the same
bundle shape described above. No login page is hand-constructed and no token
is reverse-engineered; the Asgardeo SDK itself builds the PKCE
challenge/state/redirect_uri, same as it does for a real user. This is what
makes session capture safe to run unattended (a pre-push hook, a CI step)
instead of a one-time manual capture.

Requires the local docker-compose stack already running and reachable — the
webapp and mock-oidc (`docker ps`; see `apps/csm-portal/README.md`) — and
targets whichever port is actually published (`http://localhost:3001` by
default; override with `E2E_BASE_URL`). It deliberately has its own
`playwright.auth.config.ts` (not a project inside the main
`playwright.config.ts`) with no `webServer` block, so it never tries to boot
`pnpm run dev` itself — it only ever points at a stack you already started.

```bash
# Defaults (jane.doe@example.com / cs_engineer / crApprover.json) — jane.doe
# is a user entity-service's local seed data already recognizes; GET
# /users/me resolves a caller's roles/groups/teams from entity-service's own
# stored record for that email, NOT from the ID token's groups claim, so an
# email with no matching entity-service user 404s here regardless of what's
# typed into mock-oidc's Groups field. Pick (or ask the entity-service owner
# to seed) an email that already exists there.
pnpm run test:e2e:auth

# A specific identity/role
E2E_AUTH_EMAIL=jane.doe@example.com E2E_AUTH_GROUPS=cs_engineer E2E_AUTH_ROLE=crApprover \
  pnpm run test:e2e:auth
```

Env vars (all optional):

- `E2E_AUTH_EMAIL` — the account to sign in as (default `jane.doe@example.com`,
  mock-oidc's own form default).
- `E2E_AUTH_GROUPS` — comma-separated groups sent to mock-oidc (default
  `cs_engineer`). Mostly relevant to mock-oidc's own ID token claims; the
  portal's actual role/team/group gating comes from entity-service's stored
  record for the email above, not from this value.
- `E2E_AUTH_ROLE` — the output filename, `tests/e2e/storageState/<role>.json`
  (default `crApprover` — the one role this script itself owns end to end;
  never `"approver"`/`"engineer"`, both captured by hand against a real
  staging backend, which a plain local-stack run of this script would
  otherwise silently overwrite with a token that only works locally). Not
  restricted to the roles `fixtures/test.ts`'s `withRole` currently knows
  about (`"approver" | "engineer" | "crApprover" | "crInternalApprover" |
  "crCustomerContact" | "crCustomerContact2"`) — generating a new name
  here is fine, but a spec can't call `withRole(test, "<newRole>")` until
  that union type is widened to include it.

Re-run any time to mint a fresh bundle — mock-oidc's tokens carry a 1-hour
TTL (see that service's own `signJWT` calls), so there's no "stale bundle"
state to clean up first; a fresh run always overwrites the file.

### Sessions for the Change Request seed personas

The Change Request lifecycle spec (`specs/operations/change-request-lifecycle.spec.ts`)
signs in as the seed's *personas* (`scripts/csm-compose/seed-entity-service.sql`; the
table is under "Local seed personas" in `entity-service/CLAUDE.md`), one captured
session per role. Mint all four against the running local stack (webapp on
`http://localhost:3001`, mock-oidc, entity-service + BFF up and seeded) from
`apps/csm-portal/webapp`:

| Role (`storageState/<role>.json`) | `E2E_AUTH_EMAIL` | Who |
|---|---|---|
| `crApprover` | `jane.doe@example.com` | internal; the requester persona (in no approval group) |
| `crInternalApprover` | `alice.perera@example.com` | internal; peer / CAB / ECAB approver (Bob Fernando and Carol Silva hold the same seats) |
| `crCustomerContact` | `dave.mendis@example.com` | external; registered contact of project 401 — answers Customer Approval / Customer Review |
| `crCustomerContact2` | `erin.jayawardena@example.com` | external; the other contact of project 401 |

```bash
mint() { # mint <role> <email local part>
  E2E_AUTH_EMAIL="$2@example.com" E2E_AUTH_ROLE="$1" E2E_AUTH_GROUPS=cs_engineer E2E_NO_WEBSERVER=1 \
    node_modules/.bin/playwright test --config=playwright.auth.config.ts
}
mint crApprover jane.doe
mint crInternalApprover alice.perera
mint crCustomerContact dave.mendis
mint crCustomerContact2 erin.jayawardena
```

(Add `E2E_BASE_URL=http://localhost:<port>` when the webapp is not on `:3001`.)

The same group (`cs_engineer`) is used for the customers on purpose: the local mock-oidc
signs in any email, and the BFF takes its permission from the JWT group; what the
entity-service then lets the person *see and do* comes from the user's stored type
(`internal` -> INTERNAL sees everything, `customer` -> EXTERNAL sees only the
projects they are a registered contact of). Each email must exist in the entity-service
seed or `GET /users/me` 404s and the mint fails. Tokens last an hour: re-run the four
`mint` lines if the spec starts failing on auth. A test whose role has no session is *skipped*, not
failed.

The seeded-fixture describes of the spec also reset the fixtures first, by piping
`seed-entity-service.sql` into the compose Postgres with `docker exec -i`
(`E2E_POSTGRES_CONTAINER`, default `csm-platform-postgres-1`; for the `csmcr` project use
`csmcr-postgres-1`) — the seed is self-healing, so that puts the `CHG-FIXED-*` rows back
to their starting state.

Two roles:

- **`approver.json`** — an account whose `GET /users/me` `roles` include
  `admin` (see `TIMECARD_ADMIN_GROUP` in `timeCardConstants.ts` — temporarily
  mapped to the real `admin` role until a dedicated time-card role exists).
  Sees the **Approvals** tab.
- **`engineer.json`** — a plain account **without** the `admin` role. Unlocks
  two things: the negative role-gating case, and (paired with `approver.json`)
  real cross-user approve/reject coverage in `approvals.spec.ts`. Optional —
  those tests skip cleanly without it.

## Capture a session (browser console)

1. Sign in to the app (`http://localhost:3001`) as the account you want.
2. Open DevTools → **Console**. If Chrome shows the self-XSS warning, type
   `allow pasting` and press Enter.
3. Run — this copies a session bundle (both stores) to your clipboard:

   ```js
   copy(JSON.stringify({
     origin: location.origin,
     localStorage: Object.fromEntries(Object.entries(localStorage)),
     sessionStorage: Object.fromEntries(Object.entries(sessionStorage)),
   }, null, 2))
   ```

4. Save it to the role's file (from `apps/csm-portal/webapp`):

   ```bash
   pbpaste > tests/e2e/storageState/approver.json
   ```

   Repeat from a plain-account tab → `engineer.json` if you want the negative test.

## Notes

- **Staleness:** the captured access token expires (~1h). If the run fails on
  auth, re-capture. (The bundle also carries the refresh token, so the SDK may
  refresh silently within a run.)
- **Secrecy:** `storageState/*.json` is git-ignored. Never commit it.
- **Config:** the app must have a working `public/config.js` for the same
  tenant/backend the session was issued against (the client-instance hash in the
  sessionStorage keys must match, which it does when the config is unchanged).
