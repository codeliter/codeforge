-- Corrections can also come from Empryo's UserPromptSubmit hook, and accepted corrections become rules.
-- SQLite cannot alter a CHECK, so observations is rebuilt as in 004. The vector table is created from Go, not here,
-- so a missing vector extension can never roll back the rules.
ALTER TABLE observations RENAME TO observations_old;

CREATE TABLE observations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    kind TEXT NOT NULL CHECK (kind IN ('correction', 'outcome')),
    text TEXT NOT NULL CHECK (length(text) BETWEEN 1 AND 2000),
    repository TEXT NOT NULL CHECK (repository <> ''),
    session_id TEXT NOT NULL DEFAULT '',
    dir TEXT NOT NULL DEFAULT '.' CHECK (length(dir) BETWEEN 1 AND 300),
    origin TEXT NOT NULL CHECK (origin IN ('cli', 'codeforge-run', 'hook')),
    status TEXT CHECK (status IN ('passed', 'failed', 'incomplete')),
    exit_code INTEGER,
    duration_ms INTEGER CHECK (duration_ms >= 0),
    created_at TEXT NOT NULL,
    reverted_at TEXT,
    CHECK (
        (kind = 'correction' AND origin IN ('cli', 'hook') AND status IS NULL AND exit_code IS NULL AND duration_ms IS NULL)
        OR (kind = 'outcome' AND origin = 'codeforge-run' AND duration_ms IS NOT NULL AND (
            (status = 'passed' AND exit_code = 0)
            OR (status = 'failed' AND exit_code <> 0)
            OR (status = 'incomplete' AND exit_code IS NULL)
        ))
        OR (kind = 'outcome' AND origin = 'hook' AND duration_ms IS NULL AND (
            (status = 'passed' AND (exit_code IS NULL OR exit_code = 0))
            OR (status = 'failed' AND (exit_code IS NULL OR exit_code <> 0))
            OR (status = 'incomplete' AND exit_code IS NULL)
        ))
    )
);

-- Corrections typed into the CLI before this version did not record where they were typed. Their scope is unknown
-- ('?'), so they stay as evidence but never create a rule for any directory.
INSERT INTO observations (id, kind, text, repository, session_id, dir, origin, status, exit_code, duration_ms, created_at, reverted_at)
SELECT id, kind, text, repository, session_id, CASE WHEN kind = 'correction' AND origin = 'cli' THEN '?' ELSE dir END, origin, status, exit_code, duration_ms, created_at, reverted_at FROM observations_old;

DROP TABLE observations_old;

CREATE INDEX observations_repository ON observations (repository, kind);

-- A rule is a correction the user accepted. It reaches the agent's prompt only through this table.
CREATE TABLE rules (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    repository TEXT NOT NULL CHECK (repository <> ''),
    dir TEXT NOT NULL DEFAULT '.' CHECK (length(dir) BETWEEN 1 AND 300),
    text TEXT NOT NULL CHECK (length(text) BETWEEN 1 AND 300),
    source_ids TEXT NOT NULL DEFAULT '',
    accepted_at TEXT NOT NULL,
    forgotten_at TEXT
);

CREATE INDEX rules_repository ON rules (repository);

-- Two launches adopting the same rule at once cannot both keep it: only one live copy per directory and text.
CREATE UNIQUE INDEX rules_live ON rules (repository, dir, text) WHERE forgotten_at IS NULL;

-- A dismissed group of corrections is not proposed again.
CREATE TABLE dismissals (
    repository TEXT NOT NULL CHECK (repository <> ''),
    key TEXT NOT NULL,
    dismissed_at TEXT NOT NULL,
    PRIMARY KEY (repository, key)
);
