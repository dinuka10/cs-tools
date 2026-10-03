# sre-alert-ingestion-service

Receives monitoring webhooks from 10 vendors, normalises each alert to the 8-field alert shape,
gives it a gap-free `ALT#########` id, writes it to the `alerts` table in Cosmos DB for Apache
Cassandra, and wakes up `sre-alert-core-service` (alerts-core). alerts-core reads the rows in id
order and turns them into incidents. This service does not create incidents and does not touch
alerts-core's own tables (`alert_cursor`, `incidents_*`, `processor_lease`).

```
vendor ──POST──▶ ingestion (transform → allocator: CAS-claim ids → insert) ──▶ alerts
                                           │                                               ▲
                                           └── POST /alertz (wake-up) ──▶ alerts-core ──reads┘
```

## What it does

- **Transforms**: one per vendor (`internal/vendors/<vendor>/`), following the ServiceNow Edge
  API mappings for that vendor. A payload the transform rejects is answered `400` and never
  claims an id.
- **Ids**: every replica claims ranges of ids from the `alert_seq` row with a lightweight
  transaction (compare-and-set), so ids never repeat across replicas. One claim covers everything
  queued at that moment (up to `allocator.max_batch`), so a burst costs a handful of transactions.
  Each replica starts a claim from the value it last set, so `alert_seq` is only read after a
  restart or a failed claim.
  Ids are only claimed for alerts that have a free writer (`allocator.write_concurrency`); the
  rest wait in the queue, unclaimed.
- **Writes**: each alert is inserted (read back as well with `store.read_back`). When Cosmos DB throttles ("Request rate is
  large"), the write is retried on the same id after the delay Cosmos asks for, until
  `store.write_deadline` (5m from the claim); throttling doesn't use up `store.insert_attempts`.
  After the deadline, or `store.insert_attempts` other failures, a `VOID: <reason>` filler row is
  written under the same id so alerts-core skips it immediately instead of waiting its gap
  timeout, and a DB-failure Chat card is posted.
- **AWS SNS subscriptions**: when an SNS topic subscribes the AWS URL, SNS first sends a
  `SubscriptionConfirmation`. The service confirms it (fetching its `SubscribeURL`, only from
  `sns.<region>.amazonaws.com`), emails the team named by `?team=` on the URL (default `Default`,
  from `AWS_SNS_SUBSCRIPTION_NOTIFICATION_CONFIG`), and answers `200` without storing an alert,
  as the ServiceNow AWS Alert API did. With no email configured and a failed confirmation it
  logs a CRITICAL error.
- **Memory**: everything accepted but not finished is capped at `allocator.queue_max_bytes`; past
  it, new webhooks get `503` at once.
- **Response**: `201` only after every alert in the request has been written.
- **Wake-up**: one `POST /alertz` to alerts-core per written batch. Calls are coalesced so at most
  one is in flight. If it fails, alerts-core's own 10-second poll still picks the rows up.
- **Chat cards** (Google Chat, cardsV2): a *rejected webhook* card (at most one per vendor + error
  class, and 10 in total, per `reject.window`) and a *DB failure* card (at most `fallback.cards_per_minute`, then one
  summary per minute). These limits are per replica, so N replicas can post up to N times as many
  cards. If Chat fails too, the full alert is logged at ERROR.

## Endpoints

| Method | Path | Answers |
|---|---|---|
| POST | `/api/wso2/v1/sre_alert_api/<vendor>` | `201` stored, `400` rejected payload, `401` auth, `404` unknown vendor, `405` wrong method, `413` body over `server.max_body_bytes`, `503` queue full / store unavailable / draining (with `Retry-After: 60`) |
| GET | `/healthz` | `200`, or `503` while shutting down. Never checks Cassandra, so a DB outage doesn't pull every replica out of rotation |
| GET | `/livez` | Always `200` while the process runs |

Vendors: `aws`, `azure`, `datadog`, `elasticsearch`, `gcp`, `icinga`, `openobserve`,
`opensearch`, `prometheus`, `site24x7`.

`servicenow` is temporary, for the parallel run: ServiceNow forwards the alerts it has already
transformed, so the body is the canonical alert itself (one object, or an array), with no mapping
or defaults applied. The original vendor stays in `source`. Remove the route once the vendors
point here directly.

```json
{"service":"svc","metric_name":"HighCPU","severity":"Critical","category":"cat",
 "environment":"production","source":"AWS","unique_identifier":"id-1","description":"..."}
```

Responses:

```json
201 {"status":"stored","alt_ids":["ALT000000123"],"count":1}
400 {"status":"rejected","error":"INVALID DATADOG ALERT PAYLOAD STRUCTURE"}
503 {"status":"unavailable","error":"alert queue full"}
```

A Prometheus request carries several alerts and gets one id per alert, in order. A Prometheus
batch whose alerts are all skipped by the transform answers `201` with `"count":0`.

## Run locally

Requires Go 1.25.5.

```sh
cp .env.example .env               # fill in the CASSANDRA_* values
cp config.toml.example config.toml # optional; built-in defaults are the same values
set -a; . ./.env; set +a
go run ./cmd/server
```

The Cassandra connection always uses TLS (as Cosmos DB requires), so a plain local Cassandra
container can't be used by the binary as it is. Point it at a **non-production** Cosmos DB
account/keyspace; never at the production `alertintegration` keyspace.

Tests:

```sh
gofmt -l .
go vet ./...
go test -race ./...

# Cassandra integration tests: a local container and a throwaway keyspace that is dropped afterwards.
docker run -d --name ingestion-cassandra -p 19042:9042 cassandra:4.1
CASSANDRA_TEST_PORT=19042 go test -race -tags integration ./internal/cassandra/
```

Docker image:

```sh
docker build -t sre-alert-ingestion-service .
docker run --rm -p 8080:8080 --env-file .env \
  -v "$PWD/config.toml:/app/config.toml:ro" -e CONFIG_PATH=/app/config.toml sre-alert-ingestion-service
```

## Environment variables

| Variable | Required | Meaning |
|---|---|---|
| `CASSANDRA_CONTACT_POINT` | yes | `<account>.cassandra.cosmos.azure.com` |
| `CASSANDRA_KEYSPACE` | yes | The keyspace alerts-core reads (`alertintegration` in production) |
| `CASSANDRA_KEY` | yes | Cosmos DB primary or secondary key (secret) |
| `CASSANDRA_USERNAME` | no | Defaults to the account name (first DNS label of the contact point) |
| `CASSANDRA_PORT` | no | Default `10350` |
| `AUTH_ENABLED` | no | `true` checks every vendor webhook against alerts-core's `integration_users` table, sent as `curl -u user:secret` or `Authorization: Bearer base64("user:secret")`; anything else gets `401`. Default `false`: every request is accepted |
| `AUTH_AUDIT_ONLY` | no | With `AUTH_ENABLED=true`: check credentials and log `auth would reject request`, but reject nothing. The rollout step, so vendors can be given credentials one at a time without dropping alerts. Default `false` |
| `ALERT_CORE_WAKE_URL` | no | alerts-core's `POST /alertz` URL. Empty: no wake-up, alerts-core's poll still works |
| `ALERT_CORE_WAKE_USERNAME`, `ALERT_CORE_WAKE_SECRET` | no | An `integration_users` credential for the wake call, sent as `Bearer base64("<user>:<secret>")` and only over https. Provision with alerts-core's `cmd/user`. The secret is a secret |
| `FALLBACK_CHAT_WEBHOOK_URLS` | no | Comma-separated Google Chat webhook URLs (secret). Empty: no cards, only logs |
| `AWS_SNS_SUBSCRIPTION_NOTIFICATION_CONFIG` | no | `{"teams":{"<team>":"<email>","Default":"<email>"}}`: who is emailed about SNS subscription confirmations, by the AWS URL's `?team=` |
| `EMAIL_BASE_URL`, `EMAIL_TOKEN_URL`, `EMAIL_CLIENT_ID`, `EMAIL_CLIENT_SECRET`, `EMAIL_FROM_ADDRESS` | no | WSO2 email notification service (OAuth2 client credentials) for those emails. `EMAIL_CLIENT_SECRET` is a secret. Empty `EMAIL_BASE_URL` disables email |
| `<VENDOR>_ALERT_CONFIG` | no* | Per-vendor JSON overrides, same keys and shapes as the ServiceNow Edge API alert-config properties, e.g. `DATADOG_ALERT_CONFIG` |
| `CONFIG_PATH` | no | Path to `config.toml`. Default `./config.toml`; a missing file means built-in defaults |
| `PORT` | no | Default `8080` |

\* `SITE24X7_ALERT_CONFIG` needs a `TagList` for Site24x7 alerts to carry service, category and
environment, for example
`{"TagList":{"Service":"svc","Category":"cat","Environment":"env"}}`. A malformed
`<VENDOR>_ALERT_CONFIG` stops the service at startup.

## Configuration (`config.toml`)

Everything is optional; [`config.toml.example`](config.toml.example) lists every key with its
default and a comment. The main knobs:

| Key | Default | Meaning |
|---|---|---|
| `server.shutdown_grace` | `25s` | Total SIGTERM budget; must cover the three below |
| `server.drain_delay` | `5s` | `/healthz` answers `503` this long before the listener closes |
| `server.request_wait` | `10s` | A request waits this long for its ids, then gets `503`; also in-flight requests' time on shutdown |
| `server.allocator_drain` | `10s` | The allocator's own time on shutdown to write everything already claimed |
| `server.write_timeout` | `30s` | Connection write limit; must be at least 1s above `request_wait` |
| `server.idle_timeout` | `60s` | Idle keep-alive connections are closed after this |
| `server.max_body_bytes` | `1048576` | Larger bodies get `413` |
| `allocator.queue_size` | `5000` | Queued submissions per replica before `503` |
| `allocator.queue_max_bytes` | `268435456` | Memory cap (256 MiB) on accepted, unfinished alerts before `503`; about half the container memory limit |
| `allocator.max_batch` | `200` | Most ids claimed in one compare-and-set |
| `allocator.write_concurrency` | `16` | Parallel inserts per replica; ids are only claimed for free writers |
| `store.insert_attempts` | `5` | Attempts for errors other than throttling before the filler row (`insert_base_delay` 250ms, doubling) |
| `store.write_deadline` | `5m` | How long, from the claim, a throttled write keeps retrying; must stay under alerts-core's `gap_timeout` (10m) |
| `store.read_back` | `false` | Read each row back before `201`. Costs 2 RU per alert; a Cosmos DB write is durable once acknowledged |
| `store.claim_timeout` | `5s` | Timeout for the `alert_seq` read and compare-and-set (inserts use `store.query_timeout`, 1.5s) |
| `reject.window` | `15m` | Rejected-webhook card window: one per vendor + error class, 10 in total, per replica |
| `fallback.cards_per_minute` | `5` | DB-failure cards per minute, per replica, before summarising |

**Why `write_deadline` is under 10 minutes:** alerts-core skips an id that stays missing for its
`gap_timeout` (10m). A write still retrying past that would land after alerts-core gave up on the
id, and the alert would be silently ignored. Stopping at 5m leaves time for the filler row.

## Example requests

`BASE=http://localhost:8080/api/wso2/v1/sre_alert_api`. The same payloads, plus a recovery
payload for each vendor, are in [`internal/vendors/testdata/`](internal/vendors/testdata/).

```sh
# AWS (SNS notification wrapping a CloudWatch alarm)
curl -sS -X POST "$BASE/aws" -H 'Content-Type: application/json' -d '{"Type":"Notification","MessageId":"7a3f9c2e-4b1d-4e8a-9c3f-2b8d5e6f1a9c","TopicArn":"arn:aws:sns:us-east-1:487629103847:prod-cloudwatch-alarms","Message":"{\"AlarmName\":\"prod-rds-cpu-utilization-high\",\"AlarmArn\":\"arn:aws:cloudwatch:us-east-1:487629103847:alarm:prod-rds-cpu-utilization-high\",\"NewStateValue\":\"ALARM\",\"NewStateReason\":\"Threshold Crossed: 1 datapoint [92.4] was greater than the threshold (90.0)\",\"AlarmDescription\":\"{\\\"service\\\":\\\"client-example-alert-integration\\\",\\\"category\\\":\\\"service_interruption\\\",\\\"environment\\\":\\\"production\\\",\\\"severity\\\":\\\"critical\\\"}\"}","Timestamp":"2026-09-24T05:12:33.512Z"}'

# Azure Monitor (common alert schema)
curl -sS -X POST "$BASE/azure" -H 'Content-Type: application/json' -d '{"schemaId":"azureMonitorCommonAlertSchema","data":{"essentials":{"alertId":"/subscriptions/4c9e2a1f-8b3d-4e7c-9f1a-2b6d8e3c5a9f/providers/Microsoft.AlertsManagement/alerts/7f3a9c2e-4b1d-4e8a-9c3f-2b8d5e6f1a9c","alertRule":"prod-app-service-response-time-high","severity":"Sev1","signalType":"Metric","monitorCondition":"Fired","monitoringService":"Platform","firedDateTime":"2026-09-24T05:12:33.481Z"},"customProperties":{"service":"client-example-alert-integration","category":"service_interruption","environment":"production"},"alertContext":{}}}'

# Datadog
curl -sS -X POST "$BASE/datadog" -H 'Content-Type: application/json' -d '{"event_name":"prod-web high memory usage","trigger_name":"avg(last_5m):avg:system.mem.pct_usable{env:production} < 0.1","transition":"Triggered","alert_id":"148502937","service":"client-example-alert-integration","category":"service_interruption","tags":"env:production,severity:1,team:sre"}'

# Elasticsearch
curl -sS -X POST "$BASE/elasticsearch" -H 'Content-Type: application/json' -d '{"rule_id":"a8f2c9e1-3b7d-4f6a-9c1e-8d2b5f7a3c9e","rule_name":"prod-cluster-disk-watermark-exceeded","trigger_name":"disk.watermark.flood_stage","state":"ACTIVE","alert_id":"ZQ79cZ0B6qTDiYX-WKue","severity":"1","service":"client-example-alert-integration","category":"service_interruption"}'

# GCP Cloud Monitoring
curl -sS -X POST "$BASE/gcp" -H 'Content-Type: application/json' -d '{"incident":{"incident_id":"0.mzq9x7k2j8h4","state":"open","severity":"critical","policy_name":"prod-api-5xx-error-rate","condition_name":"5xx error rate above 5% for 5 minutes","resource":{"type":"gce_instance","labels":{"service":"client-example-alert-integration","category":"service_interruption","environment":"production"}},"started_at":1758700800},"version":"1.2"}'

# Icinga
curl -sS -X POST "$BASE/icinga" -H 'Content-Type: application/json' -d '{"notification_type":"PROBLEM","host_name":"prod-db-primary-01","host_display_name":"prod-db-primary-01.example.internal","host_state":"UP","service_name":"postgres-replication-lag","service_state":"CRITICAL","vars":{"service":"client-example-alert-integration","environment":"production"}}'

# OpenObserve
curl -sS -X POST "$BASE/openobserve" -H 'Content-Type: application/json' -d '{"short_description":"prod-api-gateway: p99 latency above 2000ms","description":"p99 latency has been above 2000ms for 5 minutes","urgency":"1","impact":"1","correlation_id":"9f3a7c2e-4b1d-4e8a-9c3f-2b8d5e6f1a9c","caller_id":"openobserve","service":"client-example-alert-integration","category":"service_interruption","environment":"production"}'

# OpenSearch
curl -sS -X POST "$BASE/opensearch" -H 'Content-Type: application/json' -d '{"monitor_id":"T3x9mZQBv8h5k2j4L7n1","monitor_name":"prod-cluster-jvm-heap-usage-critical","trigger_name":"jvm-heap-above-90pct","state":"ACTIVE","alert_id":"xY29Y5oB7fN3k1L8Qm4R","severity":"1","service":"client-example-alert-integration","category":"service_interruption"}'

# Prometheus Alertmanager (one id per alert in "alerts")
curl -sS -X POST "$BASE/prometheus" -H 'Content-Type: application/json' -d '{"receiver":"sre-alert-integration","status":"firing","alerts":[{"status":"firing","labels":{"alertname":"PodCrashLoopBackOff","severity":"critical","service":"client-example-alert-integration","category":"service_interruption","environment":"production","namespace":"example"},"annotations":{"summary":"Pod restarted 5 times in 10 minutes"},"startsAt":"2026-09-24T05:12:33Z","fingerprint":"a1b2c3d4e5f6a7b8"}]}'

# Site24x7 (needs SITE24X7_ALERT_CONFIG={"TagList":{"Service":"svc","Category":"cat","Environment":"env"}})
curl -sS -X POST "$BASE/site24x7" -H 'Content-Type: application/json' -d '{"STATUS":"DOWN","MONITORNAME":"prod-example-api-https-check","MONITOR_ID":"100004312589","TAGS":["svc:client-example-alert-integration","cat:service_interruption","env:production"]}'
```

## Deploying on Choreo

1. **Component**: create a *Service* component from this repo with build context
   `integrations/sre-alert-ingestion-service` and the Dockerfile build preset. The Dockerfile runs the
   tests, builds a static binary and runs it as user `10014`.
2. **Endpoints** come from [`.choreo/component.yaml`](.choreo/component.yaml):
   - `sre-alert-api`, base path `/api/wso2/v1/sre_alert_api`, Public: the vendor webhooks. Its
     resources come from [`openapi.yaml`](openapi.yaml), one `POST /<vendor>` per vendor, so the
     gateway only accepts known vendor paths. Vendors POST to `<endpoint URL>/<vendor>`. Choreo
     enables OAuth2 on new Public endpoints by default; turn it off (or set the security the
     vendors can send). Adding a vendor needs its path in `openapi.yaml` as well as its transform.
   - `healthz`, Public; use `/healthz` as the readiness probe.
   - `livez`, Project; use `/livez` as the liveness probe.
3. **Environment variables**: set everything from [Environment variables](#environment-variables).
   Mark `CASSANDRA_KEY`, `FALLBACK_CHAT_WEBHOOK_URLS` and `EMAIL_CLIENT_SECRET` as secrets.
4. **config.toml file mount**: to change any default, add a *file mount* under
   Configs & Secrets with mount path `/etc/sre-alert-ingestion-service/config.toml` and the contents
   of your edited `config.toml.example`, then set
   `CONFIG_PATH=/etc/sre-alert-ingestion-service/config.toml`. Keep it out of `/app`, where the binary
   lives, so the mount can never hide it. Without the mount the service runs on the built-in
   defaults.
5. **Connecting to alerts-core**: add a connection from this component to the
   `sre-alert-core-service` component's endpoint (Project visibility is enough) and set
   `ALERT_CORE_WAKE_URL` to that endpoint's URL plus `/alertz`. Both components must use the same
   `CASSANDRA_*` values: this service writes the `alerts` rows that alerts-core reads.
6. **Replicas**: any number. Ids stay unique across replicas because every claim is a
   compare-and-set on `alert_seq`.
7. **Cosmos DB throughput**: use **autoscale** (max 2000 RU/s) on the account. Throttled writes
   are retried, but a burst still waits on the RU budget.
8. **Memory**: set `allocator.queue_max_bytes` to about half the container memory limit.
   Queued alerts live in memory only: if the pod crashes, alerts not yet written are lost (their
   senders got no `201`).
9. **Shutdown**: on SIGTERM `/healthz` turns `503` for `drain_delay`, in-flight requests get
   `request_wait`, then the allocator gets its own `allocator_drain` so every claimed id gets its
   row (or filler), all within `server.shutdown_grace` (25s by default). **Set Choreo's
   termination grace period to 30s or more.**

## Logs

JSON on stdout, one `request` line per webhook with `request_id` (from `X-Request-ID` if sent),
`vendor`, `status`, `alt_ids`, `count`, `duration_ms` and `error`. The allocator logs
`batch claimed` (id range, timings, `queue_len`, `queue_bytes`) and `batch written` (timings,
`throttled` count, and `total_ru` when Cosmos reports request charges). Health probes are not logged.
