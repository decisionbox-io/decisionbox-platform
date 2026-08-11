-- CRM Postgres seed for the multi-warehouse E2E (#161).
-- A local/ephemeral Postgres standing in for a second same-driver datasource.
-- Deliberately uses schema `public` + table `users` so its qualified name
-- collides with Redshift TICKIT's `public.users` — the per-warehouse
-- isolation must keep the two apart.
--
-- `userid` is the shared key with TICKIT users; `flagged` marks accounts the
-- CRM has flagged, so "which of the top-N TICKIT buyers are flagged?" is a real
-- cross-datasource (multi-hop) question. The flag rule deterministically flags
-- userid % 7 == 0 plus a fixed set that overlaps the top TICKIT buyers, so the
-- multi-hop answer is non-empty and reproducible.

DROP TABLE IF EXISTS public.users;
CREATE TABLE public.users (
  userid    integer PRIMARY KEY,
  full_name text,
  segment   text,
  flagged   boolean NOT NULL DEFAULT false
);

INSERT INTO public.users (userid, full_name, segment, flagged)
SELECT g,
       'User ' || g,
       (ARRAY['smb','mid','enterprise'])[1 + (g % 3)],
       (g % 7 = 0) OR g IN (240, 3286, 16391, 5636)
FROM generate_series(1, 20000) AS g;
