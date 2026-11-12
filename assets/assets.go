package assets

import "embed"

// Profile is synced into ~/.empryo on every launch: profiles, skills and the Codeforge themes.
//
//go:embed agents skills themes
var Profile embed.FS

//go:embed personas
var Personas embed.FS

//go:embed prompts/init.md
var InitPrompt string

// PersonaCheckPrompt has a drafted persona checked against the history it was drafted from.
//
//go:embed prompts/persona-check.md
var PersonaCheckPrompt string

// PersonaSummaryPrompt draws a persona's opening and its short rules from its learned sections.
//
//go:embed prompts/persona-summary.md
var PersonaSummaryPrompt string

//go:embed prompts/review-rubric.md
var ReviewRubric string

//go:embed prompts/repo-persona.md
var RepoPersonaTemplate string

//go:embed agents/codeforge-gate.md
var Gate string

// Help text shown by `codeforge help`; edit these files, not Go strings.
//
//go:embed help/common.txt
var HelpCommon string

//go:embed help/more.txt
var HelpMore string

//go:embed help/advanced.txt
var HelpAdvanced string

//go:embed help/footer.txt
var HelpFooter string
