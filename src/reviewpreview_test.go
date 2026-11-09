package src

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src/review"
)

func TestPreview_ShowsWhatGoesOnThePR(t *testing.T) {
	post := &review.PostRequest{}
	post.Target("acme/w", "42")
	post.SetDiff("diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,0 +1,2 @@\n+x\n+y\n")
	text := "issue (blocking): a.go:1 — Close the body.\nsuggestion: a.go:300 — The retry runs twice.\n\nStellar work.\n\nDecision: Request changes"
	p, err := post.Preview(text, "")
	require.NoError(t, err)
	require.Contains(t, p.Body, "Stellar work.")
	require.NotContains(t, p.Body, "Decision:", "the Decision line is never posted")
	require.Len(t, p.Lines, 1)
	require.Len(t, p.OnFile, 1)

	a := previewView(p, review.DecisionRequestChanges, codeStyle{width: 100}, nil)
	require.Contains(t, a.Intro, "Request changes")
	require.Equal(t, "Review body", a.Items[0].Title)
	require.Equal(t, "a.go:1", a.Items[1].Title)
	require.Contains(t, a.Items[2].Summary, "on the file")
	require.Contains(t, a.Items[2].Body, "The retry runs twice.")
}
