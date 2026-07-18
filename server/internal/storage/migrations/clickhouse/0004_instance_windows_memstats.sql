-- 0004_instance_windows_memstats.sql — additive MemStats fields on instance_windows.
--
-- Six runtime.MemStats fields the client already reads per sample (near-zero cost)
-- are added to the per-instance USE stream so a slowdown can be attributed to GC
-- frequency, GC CPU, and allocation pressure without a release:
--   num_gc / total_alloc_bytes / mallocs — per-window DELTAs (summed at read time)
--   gc_cpu_fraction / heap_inuse_bytes / gomaxprocs — point-in-time gauges.
--
-- Migrations run in lexical order on EVERY startup, so each statement is idempotent
-- (ADD COLUMN IF NOT EXISTS). The same columns are declared in 0003's CREATE TABLE,
-- so on a FRESH DB these ALTERs are no-ops; on an EXISTING dev DB they add the
-- columns in place — non-destructive, no reset needed.

ALTER TABLE instance_windows ADD COLUMN IF NOT EXISTS num_gc            UInt64;
ALTER TABLE instance_windows ADD COLUMN IF NOT EXISTS total_alloc_bytes UInt64;
ALTER TABLE instance_windows ADD COLUMN IF NOT EXISTS mallocs           UInt64;
ALTER TABLE instance_windows ADD COLUMN IF NOT EXISTS gc_cpu_fraction   Float64;
ALTER TABLE instance_windows ADD COLUMN IF NOT EXISTS heap_inuse_bytes  UInt64;
ALTER TABLE instance_windows ADD COLUMN IF NOT EXISTS gomaxprocs        UInt32;
