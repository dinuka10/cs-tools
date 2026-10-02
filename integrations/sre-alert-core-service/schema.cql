-- Copyright (c) 2026, WSO2 LLC. (https://www.wso2.com).
--
-- WSO2 LLC. licenses this file to you under the Apache License,
-- Version 2.0 (the "License"); you may not use this file except
-- in compliance with the License.
-- You may obtain a copy of the License at
--
-- http://www.apache.org/licenses/LICENSE-2.0
--
-- Unless required by applicable law or agreed to in writing,
-- software distributed under the License is distributed on an
-- "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
-- KIND, either express or implied.  See the License for the
-- specific language governing permissions and limitations
-- under the License.

CREATE KEYSPACE IF NOT EXISTS alertintegration WITH replication = {
  'class': 'SimpleStrategy', 'replication_factor': 1
};

CREATE TABLE IF NOT EXISTS alertintegration.alerts (
  id         text PRIMARY KEY,
  vendor     text,
  alert      text,
  created_at timestamp
);

CREATE TABLE IF NOT EXISTS alertintegration.alert_seq (
  name text PRIMARY KEY,
  seq  bigint
) WITH cosmosdb_cell_level_timestamp = true
  AND cosmosdb_cell_level_timestamp_tombstones = true
  AND cosmosdb_cell_level_timetolive = true;

CREATE TABLE IF NOT EXISTS alertintegration.incidents_processed (
  fingerprint             text PRIMARY KEY,
  incident_id             text,       -- CSM's own incident id (uuid); empty until csm_confirmed
  incident_number         text,       -- placeholder ("PENDING-<fp>") until CSM assigns the real number
  status                  text,       -- "open"/"closed", synced from CSM once csm_confirmed; meaningless before that
  severity                int,
  impact                  text,       -- CSM's Impact enum: "HIGH" | "MEDIUM" | "LOW"
  urgency                 text,       -- CSM's Urgency enum: "HIGH" | "MEDIUM" | "LOW"
  service                 text,
  metric_name             text,
  description             text,
  category                text,
  environment             text,
  source                  text,
  alert_ids               list<text>,
  alert_count             int,
  work_notes              list<text>,
  pending_notes           list<text>, -- FIFO subset of work_notes not yet confirmed pushed to CSM
  first_seen              timestamp,
  last_seen               timestamp,
  state_checked_at        timestamp,  -- last time syncIncidentState refreshed status from CSM, throttling that call during storms
  fallback                boolean,    -- one-time Chat notification, sent only while CSM remains unconfirmed
  csm_confirmed           boolean,
  csm_attempts            int,        -- failed CreateIncident attempts against this incident
  csm_permanently_failed  boolean,    -- true once csm_attempts hits notify.max_csm_attempts, or CSM returned a non-retryable 4xx
  csm_last_attempt_at     timestamp,  -- backs exponential retry backoff during a CSM outage
  version                 bigint      -- optimistic-concurrency fence; every mutating write is IF version = <value just read>
) WITH cosmosdb_cell_level_timestamp = true
  AND cosmosdb_cell_level_timestamp_tombstones = true
  AND cosmosdb_cell_level_timetolive = true;

-- On a pre-existing incidents_processed table, run once (not part of routine `make migrate`-style setup); the server backfills version=0 on every existing row itself at startup (IncidentRepo.BackfillVersions), since a NULL column never satisfies casUpdate's "IF version = 0" equality check.
--   ALTER TABLE alertintegration.incidents_processed ADD version bigint;

-- Maintained index of incidents still owing CSM/Chat delivery work, so RetrySweep never has to scan all of incidents_processed.
CREATE TABLE IF NOT EXISTS alertintegration.incidents_pending (
  fingerprint text PRIMARY KEY
) WITH cosmosdb_cell_level_timestamp = true
  AND cosmosdb_cell_level_timestamp_tombstones = true
  AND cosmosdb_cell_level_timetolive = true;

CREATE TABLE IF NOT EXISTS alertintegration.alert_cursor (
  name text PRIMARY KEY,
  seq  bigint
) WITH cosmosdb_cell_level_timestamp = true
  AND cosmosdb_cell_level_timestamp_tombstones = true
  AND cosmosdb_cell_level_timetolive = true;

CREATE TABLE IF NOT EXISTS alertintegration.processor_lease (
  name       text PRIMARY KEY,
  owner      text,
  expires_at timestamp
) WITH cosmosdb_cell_level_timestamp = true
  AND cosmosdb_cell_level_timestamp_tombstones = true
  AND cosmosdb_cell_level_timetolive = true;

-- Internal service accounts (e.g. webhook-integration-user) that authenticate via Authorization: Bearer base64("username:secret") or Basic auth.
CREATE TABLE IF NOT EXISTS alertintegration.integration_users (
  username          text PRIMARY KEY,
  id                uuid,      -- stable identity id, generated once at creation
  secret_hash       text,      -- base64 PBKDF2-SHA256 derived key
  salt              text,      -- base64 random salt, unique per user
  iterations        int,       -- PBKDF2 iteration count used for this row
  enabled           boolean,
  created_at        timestamp, -- account creation time, stable across rotations
  created_by        text,      -- operator who provisioned it
  updated_at        timestamp, -- bumped on every create/rotate/enable/disable
  secret_rotated_at timestamp, -- bumped only when secret_hash/salt actually change
  last_used_at      timestamp, -- reserved for future RequireAuth wiring; always null today
  expires_at        timestamp  -- null = never expires
) WITH cosmosdb_cell_level_timestamp = true
  AND cosmosdb_cell_level_timestamp_tombstones = true
  AND cosmosdb_cell_level_timetolive = true;
