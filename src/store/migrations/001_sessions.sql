CREATE TABLE sessions (
    id TEXT PRIMARY KEY NOT NULL,
    mode TEXT NOT NULL CHECK (mode IN ('ticket', 'chat', 'review')),
    profile TEXT NOT NULL CHECK (profile = 'codeforge-' || mode),
    repository TEXT NOT NULL
);
