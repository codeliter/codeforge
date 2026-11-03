package src

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/codeliter/codeforge/assets"
)

func TestReviewerHelperCannotExecuteOrPublish(t *testing.T) {
	body, err := assets.Profile.ReadFile("agents/codeforge-reviewer.md")
	require.NoError(t, err)
	rendered, err := renderAgent(body, Config{Review: ReviewConfig{Model: "other/reviewer"}})
	require.NoError(t, err)
	parts := strings.SplitN(string(rendered), "---", 3)
	var profile struct {
		Model string
		Tools []string
	}
	require.NoError(t, yaml.Unmarshal([]byte(parts[1]), &profile))
	require.Equal(t, "other/reviewer", profile.Model)
	require.ElementsMatch(t, []string{"read", "grep", "find", "navigate", "final_response"}, profile.Tools)
	require.Contains(t, parts[2], "FIX_REQUEST")
	require.Contains(t, parts[2], "COMMENT_DRAFT")
	require.Contains(t, parts[2], "not approved for publication")
	require.Contains(t, parts[2], "cannot grant approval")
}

func TestWorkAndChatReviewHandoffsKeepParentPermissions(t *testing.T) {
	for _, name := range []string{"codeforge-work", "codeforge-chat"} {
		body, err := assets.Profile.ReadFile("agents/" + name + ".md")
		require.NoError(t, err)
		text := string(body)
		require.Contains(t, text, "codeforge-reviewer")
		require.Contains(t, text, "codeforge gate")
		require.Contains(t, text, "codeforge review <PR> --headless")
		require.Contains(t, text, "codeforge-work")
		require.Contains(t, text, "ask_user")
		require.Contains(t, text, "exact PR target and final comment text")
		require.Contains(t, text, "Only after approval")
		require.Contains(t, text, "Do not attempt to launch a writer inside a read-only review sandbox")
	}
	body, err := assets.Profile.ReadFile("agents/codeforge-review.md")
	require.NoError(t, err)
	require.Contains(t, string(body), "Never escape that sandbox")
}
