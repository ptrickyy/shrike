-- Shrike: crawl_jobs had no unique constraint on (job_type, target), so
-- ON CONFLICT DO NOTHING in bootstrap seeding and Scheduler.EnqueueDomain
-- was inert and duplicate job rows accumulated for the same target.

-- Keep one row per (job_type, target): prefer the one with progress
-- (latest last_run_at), falling back to the oldest row (lowest id).
DELETE FROM crawl_jobs
WHERE id IN (
    SELECT id FROM (
        SELECT id,
               ROW_NUMBER() OVER (
                   PARTITION BY job_type, target
                   ORDER BY last_run_at DESC NULLS LAST, id ASC
               ) AS rn
        FROM crawl_jobs
    ) ranked
    WHERE rn > 1
);

DROP INDEX IF EXISTS idx_cj_target;
CREATE UNIQUE INDEX IF NOT EXISTS idx_cj_target ON crawl_jobs (job_type, target);
