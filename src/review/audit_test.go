package review

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const auditDiff = `diff --git a/src/levels.go b/src/levels.go
--- a/src/levels.go
+++ b/src/levels.go
@@ -10,3 +10,9 @@ func pick(a, b shape) int {
 	// the newest first
+	if (da <= 0) != (db <= 0) {
+		return -1
+	}
+	if c := compareVersions(sb.version, sa.version); c != 0 {
+	name := "plain assignment"
+	// a comment with < and >
 	return 0
diff --git a/src/levels_test.go b/src/levels_test.go
--- a/src/levels_test.go
+++ b/src/levels_test.go
@@ -1,1 +1,2 @@
+	require.True(t, a < b)
diff --git a/README.md b/README.md
--- a/README.md
+++ b/README.md
@@ -1,1 +1,2 @@
+Use a <= b.
`

func TestAuditLines_ListsAddedLogicLinesInCode(t *testing.T) {
	require.Equal(t, []string{
		"src/levels.go:11: if (da <= 0) != (db <= 0) {",
		"src/levels.go:12: return -1",
		"src/levels.go:14: if c := compareVersions(sb.version, sa.version); c != 0 {",
	}, AuditLines(auditDiff), "comments, plain assignments, tests and docs are left out")
}

func TestAuditSection(t *testing.T) {
	got := auditSection(auditDiff)
	require.Contains(t, got, "<check_each_line>")
	require.Contains(t, got, "bugs cluster")
	require.Contains(t, got, "src/levels.go:14: if c := compareVersions")
	require.Empty(t, auditSection("+++ b/README.md\n@@ -1 +1 @@\n+text\n"))
}

// The seeded defects of the large evals are checked against the checklist in TestEvalAuditCoversSeededDefects.
