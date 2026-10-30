//nolint:err113 // dynamic error messages are okay in tests
package src

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const listing = `GitHub Copilot (copilot)
  source: live /models listing for your seat
  copilot/claude-haiku-5.5  1000k ctx  $0.1/$0.5/M
  copilot/claude-opus-5.5  1000k ctx  $4/$20/M
  copilot/gpt-6-luna  1000k ctx  $0.1/$0.5/M
  copilot/gpt-6.1-sol  1050k ctx  $2/$10/M
  copilot/kimi-k3  1049k ctx  $3/$15/M
  copilot/gpt-5.5  272k ctx  $5/$30/M

Anthropic (anthropic)
  anthropic/claude-opus-5.5  1000k ctx  $4/$20/M
`

func TestParseModelList(t *testing.T) {
	got := parseModelList(strings.NewReader(listing))
	require.Len(t, got, 7)
	require.Equal(t, "copilot/claude-haiku-5.5", got[0])
	require.NotContains(t, got, "/models")
}

func TestRecommend(t *testing.T) {
	rec := recommend(parseModelList(strings.NewReader(listing)))

	require.Equal(t, []string{"copilot/claude-opus-5.5"}, rec["model"])
	require.Equal(t, []string{"copilot/claude-haiku-5.5", "copilot/gpt-6-luna"}, rec["judges"])
	require.Equal(t, []string{"copilot/gpt-6.1-sol"}, rec["review.model"])
	require.Equal(t, []string{"copilot/kimi-k3"}, rec["review.second_opinion"])
	require.Equal(t, []string{"copilot/claude-opus-5.5"}, rec["init"])
}

func TestRecommend_ReviewersAvoidCoderFamily(t *testing.T) {
	rec := recommend([]string{"x/claude-opus-5.5", "x/gpt-6.1-sol", "x/gpt-5.5", "x/kimi-k3", "x/gemini-4-argon"})

	require.Equal(t, []string{"x/claude-opus-5.5"}, rec["model"])
	require.Equal(t, []string{"x/gpt-6.1-sol"}, rec["review.model"])
	require.Equal(t, []string{"x/kimi-k3"}, rec["review.second_opinion"])
}

func TestRecommend_NothingAvailable(t *testing.T) {
	require.Empty(t, recommend([]string{"x/unknown"}))
}

func TestApply(t *testing.T) {
	var cfg Config
	require.NoError(t, apply(&cfg, "model", []string{"a/b"}))
	require.NoError(t, apply(&cfg, "review.second_opinion", []string{"c/d", "e/f"}))
	require.Equal(t, "a/b", cfg.Model)
	require.Equal(t, []string{"c/d", "e/f"}, cfg.Review.SecondOpinion)

	require.ErrorIs(t, apply(&cfg, "nope", []string{"a/b"}), errUnknownRole)
	require.ErrorIs(t, apply(&cfg, "model", []string{"a/b", "c/d"}), errOneModel)
	require.ErrorIs(t, apply(&cfg, "judges", []string{"plain"}), errNotProviderModel)
	require.ErrorIs(t, apply(&cfg, "review.model", []string{"a/b"}), errReviewerIsCoder)
	require.ErrorIs(t, apply(&cfg, "review.model", []string{"c/d"}), errSecondOpinionIsReviewer)
}

func TestRecommendKeeping_AvoidsPreservedCoderFamily(t *testing.T) {
	avail := []string{"x/claude-opus-5.5", "x/gpt-6.1-sol", "x/kimi-k3"}
	rec := recommendKeeping(avail, Config{Model: "x/gpt-6.1-sol"})

	require.Equal(t, []string{"x/gpt-6.1-sol"}, rec["model"])
	require.Equal(t, []string{"x/claude-opus-5.5"}, rec["review.model"])
	require.NotContains(t, rec["review.second_opinion"], "x/gpt-6.1-sol")
}
