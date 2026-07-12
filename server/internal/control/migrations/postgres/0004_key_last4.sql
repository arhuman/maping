-- 0004_key_last4.sql — store the last 4 characters of each ingest key's secret
-- so the dashboard key list is legible (e.g. "····a91f"). Display-only: the
-- secret itself is still never stored, only its sha256 and this fragment.
-- Idempotent (ADD COLUMN IF NOT EXISTS) so it is safe to re-apply on every boot.
ALTER TABLE ingest_keys ADD COLUMN IF NOT EXISTS last4 text NOT NULL DEFAULT '';
