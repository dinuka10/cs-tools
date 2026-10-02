-- Add shard_key to csm_migration_job so the unique-pending index can
-- distinguish multiple concurrent shard jobs for the same table+type.
-- Non-sharded jobs keep the default '' and behave exactly as before.
ALTER TABLE csm_migration_job ADD COLUMN IF NOT EXISTS shard_key TEXT NOT NULL DEFAULT '';

DROP INDEX IF EXISTS idx_migration_job_unique_pending;
CREATE UNIQUE INDEX IF NOT EXISTS idx_migration_job_unique_pending
    ON csm_migration_job (table_name, job_type, shard_key)
    WHERE status IN ('pending', 'running');
