-- Outcomes can also come from Empryo's tool hooks: the hook sees whether a command's tool call succeeded or
-- failed, and the exit code only when Empryo reports it. SQLite cannot alter a CHECK, so the table is rebuilt.
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
        (kind = 'correction' AND origin = 'cli' AND status IS NULL AND exit_code IS NULL AND duration_ms IS NULL)
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

INSERT INTO observations (id, kind, text, repository, session_id, origin, status, exit_code, duration_ms, created_at, reverted_at)
SELECT id, kind, text, repository, session_id, origin, status, exit_code, duration_ms, created_at, reverted_at FROM observations_old;

DROP TABLE observations_old;

CREATE INDEX observations_repository ON observations (repository, kind);
