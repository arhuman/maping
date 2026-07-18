-- 0006_instance_windows_congestion.sql — process-level congestion gauges.
--
-- Three point-in-time gauges the client reads per sample are added to the
-- per-instance USE stream so a "blocked, not busy" slowdown (latency up while
-- CPU/GC stay flat) can be read:
--   open_fds — open file-descriptor count (Linux /proc/self/fd entries); a rising
--     count proxies leaking or accumulating connections/files. 0 where unavailable.
--   fd_limit — the soft RLIMIT_NOFILE ceiling, so nearness to the limit is
--     computable (open_fds over fd_limit). 0 where unavailable.
--   in_flight — the window's PEAK request concurrency, showing the process backing
--     up when it is blocked rather than busy.
--
-- Migrations run in lexical order on EVERY startup, so each statement is idempotent
-- (ADD COLUMN IF NOT EXISTS). The same columns are declared in 0003's CREATE TABLE,
-- so on a FRESH DB these ALTERs are no-ops; on an EXISTING dev DB they add the
-- columns in place — non-destructive, no reset needed.

ALTER TABLE instance_windows ADD COLUMN IF NOT EXISTS open_fds  UInt64;
ALTER TABLE instance_windows ADD COLUMN IF NOT EXISTS fd_limit  UInt64;
ALTER TABLE instance_windows ADD COLUMN IF NOT EXISTS in_flight UInt64;
