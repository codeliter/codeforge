package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/src/forms"
)

func TestBuild_RendersEveryKindAndReadsTheAnswers(t *testing.T) {
	f := forms.Form{Name: "all kinds", Groups: []forms.Group{
		{Fields: []forms.Field{
			{Key: "scope", Kind: forms.Select, Title: "Where?", Description: "Pick one.", Options: []forms.Option{{Label: "This project: /work", Value: "project"}, {Label: "All projects", Value: "global"}}, Value: "global"},
			{Key: "models", Kind: forms.MultiSelect, Title: "Models", Options: []forms.Option{{Label: "a", Value: "a"}, {Label: "b", Value: "b"}}, Values: []string{"b"}, Required: "pick at least one"},
			{Key: "save", Kind: forms.Confirm, Title: "Save this provider order?", Description: "Primary: llmgateway", Yes: true, Affirmative: "Trust it", Negative: "Skip"},
			{Key: "ticket", Kind: forms.Input, Title: "Ticket"},
			{Key: "notes", Kind: forms.Text, Title: "Notes", Lines: 3, Required: "enter a description"},
		}},
		{Fields: []forms.Field{{Key: "starter", Kind: forms.Select, Title: "Starter persona", Options: []forms.Option{{Label: "oss", Value: "oss"}}}}, When: &forms.When{Key: "scope", Is: []string{"project"}}},
	}}
	form, answers := build(f)
	form = form.WithWidth(100)
	form.Init()
	view := form.View()
	for _, want := range []string{"Where?", "Pick one.", "This project: /work", "All projects"} {
		require.Contains(t, view, want)
	}
	got := answers()
	require.Equal(t, "global", got.String("scope"), "the offered answer is the answer until the user changes it")
	require.Equal(t, []string{"b"}, got.Strings("models"))
	require.True(t, got.Bool("save"))
	require.Equal(t, "", got.String("ticket"))
	require.Len(t, got, 6, "every field answers, hidden pages included")
}

func TestState_DiffersAndRequired(t *testing.T) {
	coder := "x"
	s := state{strings: map[string]*string{"model": &coder}, lists: map[string]*[]string{}, bools: map[string]*bool{}}
	reviewer := forms.Field{Key: "review", Kind: forms.Select, Differs: []forms.Differ{{Key: "model", Message: "the reviewer must differ from the coder"}}}
	require.EqualError(t, s.differs(reviewer, "x"), "the reviewer must differ from the coder")
	require.NoError(t, s.differs(reviewer, "y"))
	second := forms.Field{Key: "second", Kind: forms.MultiSelect, Required: "no models given", Differs: []forms.Differ{{Key: "model", Message: "not the coder"}}}
	require.EqualError(t, forms.Validate(second, "", nil), "no models given")
	require.EqualError(t, s.differs(second, "y", "x"), "not the coder")
}
