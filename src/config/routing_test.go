package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEffectiveRouting(t *testing.T) {
	cfg := Config{Model: "a/coder", Judges: []string{"a/small", "b/small"}, Review: ReviewConfig{Model: "b/reviewer"}}
	require.Equal(t, map[string]string{
		"research": "a/small", "explore": "a/small", "web-search": "a/small", "summaries": "a/small",
		"verify": "b/reviewer", "review": "b/reviewer", "goal-review": "b/reviewer",
	}, EffectiveRouting(cfg), "editing, cleanup and marionette stay on the session's model")

	cfg.Routing = map[string]string{"review": "a/coder", "research": "", "editing": "c/editor"}
	got := EffectiveRouting(cfg)
	require.Equal(t, "a/coder", got["review"], "what the configuration names wins")
	require.NotContains(t, got, "research", "an empty entry keeps the job on native Empryo routing")
	require.Equal(t, "c/editor", got["editing"])
	require.Equal(t, "a/small", got["explore"])

	project := Merge(Config{Judges: cfg.Judges, Review: cfg.Review}, Config{Routing: map[string]string{"research": "", "verify": ""}})
	got = EffectiveRouting(project)
	require.NotContains(t, got, "research", "a project's reset survives the merge and opts the job out")
	require.NotContains(t, got, "verify")
	require.Equal(t, "a/small", got["explore"])

	unresolved := Config{Judges: []string{"Haiku 5.5"}, Review: ReviewConfig{Model: "GPT 6.1 Sol"}}
	require.Empty(t, EffectiveRouting(unresolved), "a name not yet resolved to an ID routes nothing")

	for job := range EffectiveRouting(Config{Judges: []string{"a/b"}, Review: ReviewConfig{Model: "c/d"}}) {
		require.Contains(t, RoutingJobs, job)
	}
}
