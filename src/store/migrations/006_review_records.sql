-- What codeforge posted on a pull request for a GitHub account, so that account's later review of it is a follow-up
-- round: the commit it reviewed, its decision, which findings and threads block, and the GitHub reviews it posted. One
-- row per account and pull request, so two accounts on one machine never share a history; the record itself is json,
-- read and written whole inside one transaction, so two runs on the same PR never lose what the other kept.
CREATE TABLE review_records (
    account TEXT NOT NULL CHECK (length(account) BETWEEN 1 AND 100),
    repo TEXT NOT NULL CHECK (length(repo) BETWEEN 3 AND 200 AND instr(repo, '/') > 1),
    number TEXT NOT NULL CHECK (length(number) BETWEEN 1 AND 20),
    record TEXT NOT NULL CHECK (json_valid(record)),
    saved_at TEXT NOT NULL,
    PRIMARY KEY (account, repo, number)
);
