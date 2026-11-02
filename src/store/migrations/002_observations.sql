CREATE TABLE observations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    kind TEXT NOT NULL CHECK (kind IN ('correction', 'outcome')),
    text TEXT NOT NULL CHECK (length(text) BETWEEN 1 AND 2000),
    repository TEXT NOT NULL CHECK (repository <> ''),
    session_id TEXT NOT NULL DEFAULT '',
    origin TEXT NOT NULL CHECK (origin IN ('cli', 'codeforge-run')),
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
    )
);

CREATE INDEX observations_repository ON observations (repository, kind);
