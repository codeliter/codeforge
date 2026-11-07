package main

import (
	"slices"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"

	"github.com/codeliter/codeforge/src/forms"
)

// state holds every field's answer while the form runs, so a later field can check an earlier one.
type state struct {
	strings map[string]*string
	lists   map[string]*[]string
	bools   map[string]*bool
}

// chosen is what the user has picked in the field key so far: one value or several.
func (s state) chosen(key string) []string {
	if v, ok := s.strings[key]; ok && *v != "" {
		return []string{*v}
	}
	if v, ok := s.lists[key]; ok {
		return *v
	}
	return nil
}

func (s state) current(key string) string {
	if v, ok := s.strings[key]; ok {
		return *v
	}
	return ""
}

// build turns the form into huh groups bound to a state, and returns how to read the answers out of it.
func build(f forms.Form) (*huh.Form, func() forms.Answers) {
	s := state{strings: map[string]*string{}, lists: map[string]*[]string{}, bools: map[string]*bool{}}
	var keys []string
	var groups []*huh.Group
	for _, g := range f.Groups {
		var fields []huh.Field
		for _, fd := range g.Fields {
			if !slices.Contains(keys, fd.Key) {
				keys = append(keys, fd.Key)
			}
			fields = append(fields, s.field(fd))
		}
		group := huh.NewGroup(fields...)
		if when := g.When; when != nil {
			group = group.WithHideFunc(func() bool { return !when.Shown(s.current) })
		}
		groups = append(groups, group)
	}
	form := huh.NewForm(groups...).WithProgramOptions(tea.WithAltScreen())
	return form, func() forms.Answers {
		out := forms.Answers{}
		for _, key := range keys {
			a := forms.Answer{}
			if v, ok := s.strings[key]; ok {
				a.Value = *v
			}
			if v, ok := s.lists[key]; ok {
				a.Values = *v
			}
			if v, ok := s.bools[key]; ok {
				a.Yes = *v
			}
			out[key] = a
		}
		return out
	}
}

func options(opts []forms.Option) []huh.Option[string] {
	out := make([]huh.Option[string], 0, len(opts))
	for _, o := range opts {
		out = append(out, huh.NewOption(o.Label, o.Value))
	}
	return out
}

// differs is the complaint when a value repeats a choice made in one of the fields the field must differ from.
func (s state) differs(fd forms.Field, values ...string) error {
	for _, d := range fd.Differs {
		for _, other := range s.chosen(d.Key) {
			if slices.Contains(values, other) {
				return forms.ComplaintError(d.Message)
			}
		}
	}
	return nil
}

// text is the answer a text-valued field binds to; fields that share a key share it, and the first one's offer starts it.
func (s state) text(fd forms.Field) *string {
	if v, ok := s.strings[fd.Key]; ok {
		return v
	}
	v := fd.Value
	s.strings[fd.Key] = &v
	return &v
}

func (s state) field(fd forms.Field) huh.Field {
	switch fd.Kind {
	case forms.Select:
		v := s.text(fd)
		return huh.NewSelect[string]().Title(fd.Title).Description(fd.Description).Options(options(fd.Options)...).Value(v).
			Validate(func(value string) error { return s.differs(fd, value) })
	case forms.MultiSelect:
		v := slices.Clone(fd.Values)
		s.lists[fd.Key] = &v
		return huh.NewMultiSelect[string]().Title(fd.Title).Description(fd.Description).Options(options(fd.Options)...).Value(&v).
			Validate(func(values []string) error {
				if err := forms.Validate(fd, "", values); err != nil {
					return err
				}
				return s.differs(fd, values...)
			})
	case forms.Confirm:
		v := fd.Yes
		s.bools[fd.Key] = &v
		c := huh.NewConfirm().Title(fd.Title).Description(fd.Description).Value(&v)
		if fd.Affirmative != "" {
			c = c.Affirmative(fd.Affirmative)
		}
		if fd.Negative != "" {
			c = c.Negative(fd.Negative)
		}
		return c
	case forms.Text:
		t := huh.NewText().Title(fd.Title).Description(fd.Description).Value(s.text(fd)).
			Validate(func(value string) error { return forms.Validate(fd, value, nil) })
		if fd.Lines > 0 {
			t = t.Lines(fd.Lines)
		}
		return t
	case forms.Input:
	}
	return huh.NewInput().Title(fd.Title).Description(fd.Description).Value(s.text(fd)).
		Validate(func(value string) error { return forms.Validate(fd, value, nil) })
}
