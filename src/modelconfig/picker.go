package modelconfig

import (
	"errors"
	"strconv"
	"strings"

	"github.com/codeliter/codeforge/src/config"
	"github.com/codeliter/codeforge/src/forms"
)

// roleField is how one model role reads on the configure screen.
type roleField struct {
	key, title, description string
	multi                   bool
}

var roleFields = []roleField{
	{key: "model", title: "Coding model", description: "Writes code in work, chat and ticket mode."},
	{key: "expert", title: "Expert delegate (on demand)", description: "Use Opus for difficult delegated work, not routine calls."},
	{key: "judges", title: "Task sizing models", description: "Fast helpers that choose how much thinking a task needs.", multi: true},
	{key: "review.model", title: "Review model", description: "Checks changes with a different model from the coder."},
	{key: "review.second_opinion", title: "Extra reviewers", description: "Second opinions for larger or riskier changes.", multi: true},
	{key: "init", title: "Persona model", description: "Learns your conventions during init."},
}

// What the configure screen offers besides the roles.
const (
	pickSave = "save"
	// savedGroup heads the saved names no connected provider serves under that name.
	savedGroup = "saved"
)

// rolesField is the configure screen: every role with the models it has now, then what to do with them. note, when
// set, is why the last attempt to save was refused.
func rolesField(cfg config.Config, note string) forms.Field {
	var options []forms.Option
	for _, role := range roleFields {
		options = append(options, forms.Option{Label: role.title, Detail: strings.Join(config.Current(cfg, role.key), ", "), Value: role.key, Group: "Roles"})
	}
	options = append(options, forms.Option{Label: "Save", Detail: "shows the resolved plan and asks first", Value: pickSave, Group: "Then"})
	description := "Enter on a role changes its model. Save when every role is right; Esc leaves without saving."
	if note != "" {
		description = "Not saved: " + note
	}
	return forms.Field{Kind: forms.Search, Title: "Configure models", Description: description, Header: "Role\tModel", Options: options}
}

// modelField is the model picker for one role: every connected provider as a group, its models under it with their
// context sizes, and a tick on what the role has now.
func modelField(role roleField, cfg config.Config, catalog []string, contexts map[string]string) forms.Field {
	current := config.Current(cfg, role.key)
	options := pickerOptions(catalog, cfg.Providers, contexts, current...)
	providers := make(map[string]bool)
	for _, o := range options {
		providers[o.Group] = true
	}
	f := forms.Field{
		Kind: forms.Search, Title: role.title, Header: "Model\tContext", Options: options, Multi: role.multi, Ordered: role.multi, // the first sizing model also serves the helper jobs, so order is kept
		Description: role.description + "  " + plural(len(options), "model") + " · " + plural(len(providers), "provider"),
	}
	if role.multi {
		f.Values = current
	} else if len(current) > 0 {
		f.Value = current[0]
	}
	return f
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// pickerOptions is providerModelOptions laid out for the picker: each option under the provider that serves it, with
// its context size. The models of the other connected providers follow under their own, as explicit provider/model
// IDs, so one can be chosen without changing the provider order.
func pickerOptions(catalog []string, policy *config.ProviderPolicy, contexts map[string]string, saved ...string) []forms.Option {
	options := providerModelOptions(catalog, policy, saved...)
	offered := make(map[string]bool)
	for i, o := range options {
		id := o.Value
		if !config.ValidModelID(id) {
			if resolved, err := config.CatalogModel(id, policy.PrimaryFor(id), catalog); err == nil {
				id = resolved
			}
		}
		offered[id], offered[o.Value] = true, true
		provider, _, ok := strings.Cut(id, "/")
		if !ok {
			provider = savedGroup
		}
		if o.Label == o.Value && ok {
			options[i].Label = config.ModelName(o.Value)
		}
		options[i].Group, options[i].Detail = provider, contexts[id]
	}
	for _, id := range catalog {
		if offered[id] || !config.SuggestedModel(id) {
			continue
		}
		offered[id] = true
		provider, _, _ := strings.Cut(id, "/")
		options = append(options, forms.Option{Label: config.ModelName(id), Value: id, Group: provider, Detail: contexts[id]})
	}
	return options
}

// chooseModels shows the configure screen until the user saves: a role opens its model picker and comes back with
// the choice, and a set of choices that cannot be saved is refused there, with the reason, not after the screen.
func chooseModels(cfg *config.Config, catalog []string, contexts map[string]string) error {
	note := ""
	for {
		choice, err := forms.Ask("configure models", rolesField(*cfg, note))
		if err != nil {
			return err
		}
		note = ""
		switch choice.Value {
		case pickSave:
			if err := config.Validate(*cfg); err != nil {
				note = err.Error()
				continue
			}
			return nil
		default:
			if err := chooseRole(cfg, choice.Value, catalog, contexts); err != nil {
				return err
			}
		}
	}
}

// chooseRole asks for one role's models. Leaving the picker keeps what the role had.
func chooseRole(cfg *config.Config, key string, catalog []string, contexts map[string]string) error {
	for _, role := range roleFields {
		if role.key != key {
			continue
		}
		answer, err := forms.Ask(role.title, modelField(role, *cfg, catalog, contexts))
		if errors.Is(err, forms.ErrAborted) {
			return nil
		}
		if err != nil {
			return err
		}
		models := answer.Values
		if !role.multi {
			models = []string{answer.Value}
		}
		config.AssignRole(cfg, key, models)
	}
	return nil
}
