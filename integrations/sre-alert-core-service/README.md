# Alert Core Service

Alert Core Service deduplicates incoming alerts into incidents and forwards them to
CSM via `csm-integration-service`, falling back to Google Chat when CSM doesn't
confirm. It reads alerts written by the separate `alert-ingestion` service from
Cassandra, folds them into incidents by fingerprint
(`source|service|metric_name|environment|unique_identifier`), and keeps retrying
notifications independently until they're actually delivered.

## Functionality

- **Alert discovery.** A poller compares the `alert_seq` counter against its own
  persisted `alert_cursor`, so it never needs CDC or a message queue to know what's
  new. `alert-ingestion` also posts to `POST /alertz` to wake it early; a fixed
  interval is the backstop if that ping never arrives.
- **Deduplication by fingerprint.** Every alert is normalized (severity label,
  category, defaults from `CORE_ALERT_DEFAULTS`) and folded into an incident keyed
  by `source|service|metric_name|environment|unique_identifier`. Repeated alerts for
  the same fingerprint become work notes on the existing incident instead of new
  incidents, as long as that incident is still inside its dedup window and not
  closed. A resolving alert (`OK`/`Clear`) closes out the incident's local state
  without ever creating one if none exists yet.
- **Generation reset.** If a fingerprint's incident has aged out of the dedup
  window or CSM already closed it, the next alert starts a fresh incident
  ("generation") instead of reopening the old one, after flushing any notes still
  owed to that old incident first.
- **Incident creation and sync with CSM.** New incidents get a placeholder
  `incident_number` locally until CSM confirms the real one. Severity maps to
  CSM's Impact/Urgency (`HIGH`/`MEDIUM`/`LOW`), and category maps to CSM's
  `SECURITY` / `INQUIRY` / `SERVICE_INTERRUPTION` (the default). Service labels are
  resolved to a CSM service id via a cached lookup, falling back to
  `CSM_UNKNOWN_SERVICE_ID` when there's no match. Open/closed state is always read
  back from CSM, never inferred locally, and that check is throttled
  (`state_check_interval`) so an alert storm doesn't hammer CSM with status polls.
- **Duplicate-create protection.** Before creating an incident, and again before
  every retry, the service searches CSM by `correlationId` (the dedup tag CSM
  stores on its own `correlation_id` field) so a lost create response never causes
  a duplicate. It also falls back to the older free-text search for incidents that
  predate `correlationId` support.
- **Work notes as HTML.** Notes pushed to CSM are built as escaped HTML with
  `<br>` line breaks, matching how CSM's work notes field actually renders content,
  so multi-line context (alert id, metric, source) doesn't collapse onto one line.
- **Decoupled, durable delivery.** Delivering to CSM and Chat is separate from
  processing the alert itself. An incident's delivery state (`csm_confirmed`,
  `pending_notes`, `fallback`, attempt counts) is persisted in Cassandra, so a CSM
  or Chat outage never blocks alert ingestion and never loses a note: a background
  retry sweep (`retry_sweep_interval`) keeps working through pending incidents with
  exponential backoff (`csm_retry_base_delay`, `csm_retry_multiplier`,
  `csm_retry_max_delay`) until CSM accepts them or the attempt cap
  (`max_csm_attempts`) is hit.
- **Chat fallback.** While CSM hasn't confirmed an incident, it's posted to the
  configured Google Chat webhook(s) (`FALLBACK_CHAT_WEBHOOK_URLS`) so a human still
  sees it. This fires once per incident, not on every retry, and once CSM finally
  confirms a delayed incident it's tagged `[DELAYED-CSM]` in the subject so it's
  clear the notification is a late catch-up, not a fresh occurrence.
- **Concurrency-safe processing.** Alerts are processed by a pool of
  fingerprint-sharded workers, so alerts for the same incident are always handled
  in order on the same worker while unrelated incidents process fully in parallel.
  A per-fingerprint lock also guards the read-modify-write around delivery state so
  two concurrent callers can't race on the same incident's pending notes.
- **Health and liveness endpoints.** `/healthz` checks Cassandra connectivity;
  `/livez` doesn't, so a transient DB blip triggers a readiness dip rather than a
  pod restart.

## Multi-container support

Alert Core Service is safe to run as multiple replicas (e.g. several Choreo
containers). Exactly one replica holds a Cassandra-backed, time-bounded lease
and acts as the active processor at any moment; the rest stand by. If the
active replica dies or is redeployed, a standby steals the lease once it
expires and resumes from the same durable cursor. Leadership and progress
both live in Cassandra rather than in-memory, so replicas can be added,
removed, or restarted freely, but note that duplicate or dropped alerts are
not fully impossible: lease handoff, CSM's own dedup-by-tag lookup (used
before every incident create), the shutdown drain sequence, and a `version`
column that fences every mutating write on `incidents_processed` (`IF version
= <value just read>`, so a replica whose lease already expired can't silently
overwrite a newer leader's update) all narrow those windows significantly,
they don't eliminate them under every failure mode (e.g. clock skew between
replicas). See the lease and notify packages' own doc comments for the
specific tradeoffs.

## Package layout

- `internal/engine`: the core dedup and delivery orchestration logic described
  above. Owns fingerprint locking, generation resets, and the retry sweep.
- `internal/poll`: discovers new alert ids from Cassandra and dispatches them to
  the engine on a fingerprint-sharded worker pool.
- `internal/lease`: elects one active processor across replicas via a
  Cassandra-backed, time-bounded lease.
- `internal/store`: Cassandra repositories for alerts and incidents.
- `internal/csm`: OAuth2 client for `csm-integration-service` (create incident,
  update work notes, search by correlation id, resolve service id).
- `internal/notify`: wraps the CSM client and Chat webhooks with retry/backoff and
  decides where each incident needs to go next.
- `internal/model`: the `Alert`/`Incident` shapes, severity/fingerprint
  normalization, and HTML work note formatting.
- `internal/hub`: the `/alertz` HTTP handler that just wakes the poller early.
- `internal/cassandra`: connection setup and the CAS-based sequence counter
  helpers the poller and lease both build on.
- `internal/config`: loads and validates `config.toml`.
- `internal/auth`: PBKDF2 hashing/verification, the `integration_users`
  Cassandra repository, and the `RequireAuth` middleware (not currently applied to any route).
- `cmd/server`: wires everything together and manages startup/shutdown.
- `cmd/user`: CLI to create/rotate, list, enable, and disable `integration_users` rows.

## Running it

```bash
go run ./cmd/server
go build ./... && go vet ./... && go test ./...
```

Configuration lives in `config.toml` (poll cadence, retry/backoff, lease TTL)
and environment variables (`CASSANDRA_*`, `CSM_INTEGRATION_*`, `CSM_CALLER_ID`,
`CSM_UNKNOWN_SERVICE_ID`, `FALLBACK_CHAT_WEBHOOK_URLS`); see `.env.example`.

`config.toml` itself is gitignored (see root `.gitignore`), since it's treated
as deployment config rather than source. Copy `config.toml.example` to
`config.toml` and customize as needed. Every field is required and validated
at startup (`internal/config.Config.validate`) because there are no built-in
fallback defaults if the file is missing a value or unparsable, so
`config.toml.example`'s values are a starting point to copy and edit, not
defaults this service falls back to on its own.

```bash
cp config.toml.example config.toml
```

Internal API users (`integration_users`) are documented separately in
[`PROVISION.md`](PROVISION.md).

## Choreo Deployment

`config.toml` is not baked into the image because it's supplied at runtime via
Choreo's **Manage > Configs and Secrets > File Mount**:

1. In the component's Choreo console, go to **Manage Configs and Secrets >
   File Mount** and add a new file mount.
2. Set the file **name** to `config.toml` and paste the contents of
   `config.toml.example` (customized as needed) as the file content.
3. Mount it at the path the service reads from: the working directory root
   (so it resolves as `config.toml`), or any path if you also set the
   `CONFIG_PATH` environment variable to that path.
4. Redeploy. The poller, lease, Cassandra, notify, and server tunables all
   load from this mounted file on startup.
