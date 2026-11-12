package modelconfig

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src/config"
	"github.com/codeliter/codeforge/src/forms"
)

func optionByValue(t *testing.T, options []forms.Option, value string) forms.Option {
	t.Helper()
	for _, o := range options {
		if o.Value == value {
			return o
		}
	}
	require.Failf(t, "option not offered", "%s in %v", value, options)
	return forms.Option{}
}

func TestPickerOptions_GroupsByProviderAndOffersTheOthersExplicitly(t *testing.T) {
	catalog := []string{"copilot/claude-sonnet-5.5", "copilot/gpt-6.1-sol", "llmgateway/claude-sonnet-5-5", "llmgateway/kimi-k3"}
	contexts := map[string]string{"copilot/gpt-6.1-sol": "1.1M", "llmgateway/kimi-k3": "1M"}
	options := pickerOptions(catalog, &config.ProviderPolicy{Primary: "copilot"}, contexts)

	sol := optionByValue(t, options, "gpt-6.1-sol")
	require.Equal(t, "copilot", sol.Group, "a model on the primary provider is offered by name, under that provider")
	require.Equal(t, "1.1M", sol.Detail)

	kimi := optionByValue(t, options, "llmgateway/kimi-k3")
	require.Equal(t, forms.Option{Label: "kimi-k3", Value: "llmgateway/kimi-k3", Group: "llmgateway", Detail: "1M"}, kimi,
		"another provider's model is offered as an explicit ID")
	require.Equal(t, "copilot", options[0].Group, "the primary provider's models come first")

	seen := map[string]bool{}
	for _, o := range options {
		require.False(t, seen[o.Value], o.Value)
		seen[o.Value] = true
		require.NotEmpty(t, o.Group, o.Value)
	}
}

func TestPickerOptions_WithNoProviderOrderEveryModelIsExplicit(t *testing.T) {
	options := pickerOptions([]string{"a/claude-sonnet-5.5", "b/gpt-6.1-sol"}, nil, nil)
	require.Equal(t, []forms.Option{
		{Label: "claude-sonnet-5.5", Value: "a/claude-sonnet-5.5", Group: "a"},
		{Label: "gpt-6.1-sol", Value: "b/gpt-6.1-sol", Group: "b"},
	}, options)
}

func TestRolesField_ListsEveryRoleWithItsModels(t *testing.T) {
	cfg := config.Defaults(config.Config{})
	f := rolesField(cfg, "")
	require.Equal(t, forms.Search, f.Kind)
	for _, role := range config.Roles {
		o := optionByValue(t, f.Options, role.Key)
		require.Equal(t, "Roles", o.Group)
		require.NotEmpty(t, o.Detail, role.Key)
	}
	require.Equal(t, "copilot/claude-haiku-5.5, copilot/gpt-6-luna", optionByValue(t, f.Options, "judges").Detail)
	require.Equal(t, "Then", optionByValue(t, f.Options, pickSave).Group)
	require.Len(t, f.Options, len(config.Roles)+1, "the roles and Save, nothing else")

	require.Contains(t, rolesField(cfg, config.ErrReviewerIsCoder.Error()).Description, "Not saved: review.model must differ")
}

func TestModelField_OffersWhatTheRoleHasNow(t *testing.T) {
	catalog := []string{"copilot/claude-opus-5.5", "copilot/gpt-6.1-sol", "copilot/kimi-k3"}
	cfg := config.Config{Model: "copilot/claude-opus-5.5", Review: config.ReviewConfig{Model: "copilot/gpt-6.1-sol", SecondOpinion: []string{"copilot/kimi-k3"}}}

	one := modelField(roleFields[3], cfg, catalog, nil)
	require.Equal(t, "Review model", one.Title)
	require.False(t, one.Multi)
	require.Equal(t, "copilot/gpt-6.1-sol", one.Value)
	require.Contains(t, one.Description, "3 models · 1 provider")

	several := modelField(roleFields[4], cfg, catalog, nil)
	require.True(t, several.Multi)
	require.True(t, several.Ordered, "sending the picker unchanged keeps the saved order")
	require.Equal(t, []string{"copilot/kimi-k3"}, several.Values)
}

func TestRoleFields_CoverEveryRole(t *testing.T) {
	require.Len(t, roleFields, len(config.Roles))
	for i, role := range config.Roles {
		require.Equal(t, role.Key, roleFields[i].key)
		require.Equal(t, role.Count > 1, roleFields[i].multi, role.Key)
	}
}
