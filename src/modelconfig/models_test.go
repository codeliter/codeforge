//nolint:err113 // dynamic error messages are okay in tests
package modelconfig

import (
	"strings"
	"testing"

	"github.com/codeliter/codeforge/src/config"
	"github.com/codeliter/codeforge/src/modelcatalog"

	"github.com/stretchr/testify/require"
)

const listing = `GitHub Copilot (copilot)
  source: live /models listing for your seat
  copilot/claude-haiku-5.5  1000k ctx  $0.1/$0.5/M
  copilot/claude-opus-5.5  1000k ctx  $4/$20/M
  copilot/claude-sonnet-5.5  1000k ctx  $2/$10/M
  copilot/gpt-6-luna  1000k ctx  $0.1/$0.5/M
  copilot/gpt-6.1-sol  1050k ctx  $2/$10/M
  copilot/kimi-k3  1049k ctx  $3/$15/M
  copilot/gpt-5.5  272k ctx  $5/$30/M

Anthropic (anthropic)
  anthropic/claude-opus-5.5  1000k ctx  $4/$20/M
`

func TestParseModelList(t *testing.T) {
	got := modelcatalog.ParseModels(strings.NewReader(listing))
	require.Len(t, got, 8)
	require.Equal(t, "copilot/claude-haiku-5.5", got[0])
	require.NotContains(t, got, "/models")
}

func TestRecommend(t *testing.T) {
	rec := recommend(modelcatalog.ParseModels(strings.NewReader(listing)))

	require.Equal(t, []string{"copilot/claude-opus-5.5"}, rec["model"])
	require.Equal(t, []string{"copilot/claude-opus-5.5"}, rec["expert"])
	require.Equal(t, []string{"copilot/claude-haiku-5.5", "copilot/gpt-6-luna"}, rec["judges"])
	require.Equal(t, []string{"copilot/gpt-6.1-sol"}, rec["review.model"])
	require.Equal(t, []string{"copilot/kimi-k3"}, rec["review.second_opinion"])
	require.Equal(t, []string{"copilot/claude-sonnet-5.5"}, rec["init"])
}

func TestRecommend_FableIsTheExpertOverOpusCoder(t *testing.T) {
	rec := recommend([]string{"x/claude-sonnet-5.5", "x/claude-opus-5.5", "x/claude-fable-5.2", "x/claude-haiku-5.5"})
	require.Equal(t, []string{"x/claude-opus-5.5"}, rec["model"])
	require.Equal(t, []string{"x/claude-fable-5.2"}, rec["expert"])
	require.Equal(t, []string{"x/claude-haiku-5.5"}, rec["judges"])
	require.Equal(t, []string{"x/claude-sonnet-5.5"}, rec["init"])
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
	var cfg config.Config
	require.NoError(t, config.Apply(&cfg, "model", []string{"a/b"}))
	require.NoError(t, config.Apply(&cfg, "review.second_opinion", []string{"c/d", "e/f"}))
	require.Equal(t, "a/b", cfg.Model)
	require.Equal(t, []string{"c/d", "e/f"}, cfg.Review.SecondOpinion)

	require.ErrorIs(t, config.Apply(&cfg, "nope", []string{"a/b"}), config.ErrUnknownRole)
	require.ErrorIs(t, config.Apply(&cfg, "model", []string{"a/b", "c/d"}), config.ErrOneModel)
	require.ErrorIs(t, config.Apply(&cfg, "judges", []string{"invalid model/ref"}), config.ErrNotProviderModel)
	require.ErrorIs(t, config.Apply(&cfg, "review.model", []string{"a/b"}), config.ErrReviewerIsCoder)
	require.ErrorIs(t, config.Apply(&cfg, "review.model", []string{"c/d"}), config.ErrSecondOpinionIsReviewer)
}

func TestRecommendKeeping_AvoidsPreservedCoderFamily(t *testing.T) {
	avail := []string{"x/claude-opus-5.5", "x/gpt-6.1-sol", "x/kimi-k3"}
	rec := recommendKeeping(avail, config.Config{Model: "x/gpt-6.1-sol"})

	require.Equal(t, []string{"x/gpt-6.1-sol"}, rec["model"])
	require.Equal(t, []string{"x/claude-opus-5.5"}, rec["review.model"])
	require.NotContains(t, rec["review.second_opinion"], "x/gpt-6.1-sol")
}
