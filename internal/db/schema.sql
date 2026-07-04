-- Searcher bot schema (Postgres). Applied on startup (idempotent).
--
-- PRIVACY: we deliberately do NOT store what users searched for — no query text
-- anywhere. We keep only counts and timestamps (for "peak hour" stats). The
-- ALTER ... DROP COLUMN IF EXISTS lines migrate any earlier schema that did.
--
-- Identity, presence and language now live in the shared "core" Postgres
-- (see internal/core), keyed on the global Telegram id. The old local `users`
-- table (which duplicated identity + held the chosen language) is retired:
-- searches/selections reference user_id directly with no FK.

-- Never let a held lock wedge startup: bound each DDL statement at the DB level
-- (the caller also applies a context deadline).
SET lock_timeout = '5s';
SET statement_timeout = '10s';

-- Retire the local users table — identity + language moved to core.
DROP TABLE IF EXISTS users;

-- One row per answered search — counts and time only, never the query text.
CREATE TABLE IF NOT EXISTS searches (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id      BIGINT NOT NULL,
    category     TEXT,                          -- images | videos | mixed
    result_count INT    NOT NULL DEFAULT 0,
    duration_ms  INT,
    source       TEXT,                          -- inline | dm
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE searches DROP COLUMN IF EXISTS query;
CREATE INDEX IF NOT EXISTS idx_searches_user    ON searches (user_id);
CREATE INDEX IF NOT EXISTS idx_searches_created ON searches (created_at);

-- One row per result the user picked/sent — type + engine only, no query/title/url.
CREATE TABLE IF NOT EXISTS selections (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id     BIGINT NOT NULL,
    result_type TEXT,                           -- photo | video
    engine      TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE selections DROP COLUMN IF EXISTS query;
ALTER TABLE selections DROP COLUMN IF EXISTS title;
ALTER TABLE selections DROP COLUMN IF EXISTS source_url;
CREATE INDEX IF NOT EXISTS idx_selections_user ON selections (user_id);
