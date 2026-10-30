package assets

import "embed"

// Profile is synced into ~/.empryo on every launch.
//
//go:embed agents skills
var Profile embed.FS

//go:embed personas
var Personas embed.FS

//go:embed prompts/init.md
var InitPrompt string

//go:embed prompts/rubric.md
var Rubric string

//go:embed prompts/repo-persona.md
var RepoPersonaTemplate string

//go:embed agents/codeforge-gate.md
var Gate string
