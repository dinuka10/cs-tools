-- Add job_type to csm_migration_row_error so errors can be filtered and
-- cleared by the job type that produced them, without joining csm_migration_job.
-- Existing rows get an empty string default (historical data, type unknown).
ALTER TABLE csm_migration_row_error
    ADD COLUMN IF NOT EXISTS job_type TEXT NOT NULL DEFAULT '';
