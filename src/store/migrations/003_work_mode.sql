CREATE TABLE sessions_with_work (
    id TEXT PRIMARY KEY NOT NULL,
    mode TEXT NOT NULL CHECK (mode IN ('ticket', 'chat', 'work', 'review')),
    profile TEXT NOT NULL CHECK (profile = 'codeforge-' || mode),
    repository TEXT NOT NULL
);

INSERT INTO sessions_with_work (id, mode, profile, repository)
SELECT id, mode, profile, repository FROM sessions;

DROP TABLE sessions;
ALTER TABLE sessions_with_work RENAME TO sessions;
