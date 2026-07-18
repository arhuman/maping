-- 0005_instance_windows_post_gc_heap.sql — post-GC heap baseline and true RSS.
--
-- Two point-in-time gauges the client already reads per sample are added to the
-- per-instance USE stream so a memory rise can be read correctly:
--   post_gc_heap_bytes — live heap as of the last GC mark (/gc/heap/live:bytes),
--     the post-GC baseline; a monotonic rise distinguishes a leak from a burst.
--   rss_true_bytes — true OS resident set size (Linux /proc/self/statm), unlike
--     rss_bytes which is runtime Sys (reserved address space). 0 where unavailable.
--
-- Migrations run in lexical order on EVERY startup, so each statement is idempotent
-- (ADD COLUMN IF NOT EXISTS). The same columns are declared in 0003's CREATE TABLE,
-- so on a FRESH DB these ALTERs are no-ops; on an EXISTING dev DB they add the
-- columns in place — non-destructive, no reset needed.

ALTER TABLE instance_windows ADD COLUMN IF NOT EXISTS post_gc_heap_bytes UInt64;
ALTER TABLE instance_windows ADD COLUMN IF NOT EXISTS rss_true_bytes     UInt64;
