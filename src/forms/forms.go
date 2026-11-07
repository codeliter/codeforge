// Package forms describes the questions codeforge asks at the terminal and has them asked by codeforge-forms, a
// separate program that holds the terminal UI library. Keeping that library out of codeforge itself spares every run,
// hooks included, the library's start-up query of the terminal, which costs five seconds wherever the terminal does
// not answer it.
package forms

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// Kind is what a field asks for.
type Kind string

// The field kinds.
const (
	// Select picks one of Options; the answer is its Value.
	Select Kind = "select"
	// MultiSelect picks any number of Options; the answer is their Values.
	MultiSelect Kind = "multiselect"
	// Confirm is a yes or no; the answer is Yes.
	Confirm Kind = "confirm"
	// Input is one line of text; the answer is its Value.
	Input Kind = "input"
	// Text is several lines of text; the answer is its Value.
	Text Kind = "text"
)

// Option is one choice of a Select or MultiSelect.
type Option struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// Differ says a field's choice must not repeat the choice made in the field Key, with Message as the complaint.
type Differ struct {
	Key     string `json:"key"`
	Message string `json:"message"`
}

// Field is one question. Value, Values and Yes carry the answer offered first and, when the form is done, the one given.
// Two fields on different pages may share a Key: they then share the answer, and the first one's offer is the initial.
type Field struct {
	Key         string   `json:"key"`
	Kind        Kind     `json:"kind"`
	Title       string   `json:"title,omitempty"`
	Description string   `json:"description,omitempty"`
	Options     []Option `json:"options,omitempty"`
	Value       string   `json:"value,omitempty"`
	Values      []string `json:"values,omitempty"`
	Yes         bool     `json:"yes,omitempty"`
	// Affirmative and Negative label a Confirm's two answers; empty means Yes and No.
	Affirmative string `json:"affirmative,omitempty"`
	Negative    string `json:"negative,omitempty"`
	// Lines is the height of a Text field.
	Lines int `json:"lines,omitempty"`
	// Required, when set, is the complaint for an empty answer: a blank Input or Text, or a MultiSelect with nothing picked.
	Required string `json:"required,omitempty"`
	// Differs lists fields whose choice this one must not repeat.
	Differs []Differ `json:"differs,omitempty"`
	// Check names a check the helper runs on the answer; see Check.
	Check string `json:"check,omitempty"`
}

// When shows a group only while the field Key holds one of Is, or none of IsNot, and And (if any) holds too.
type When struct {
	Key   string   `json:"key"`
	Is    []string `json:"is,omitempty"`
	IsNot []string `json:"is_not,omitempty"`
	And   *When    `json:"and,omitempty"`
}

// Group is one page of a form.
type Group struct {
	Fields []Field `json:"fields"`
	When   *When   `json:"when,omitempty"`
}

// Form is what the user is asked, page by page, on the alternate screen. A form with an Accordion shows that instead.
type Form struct {
	Name      string     `json:"name"`
	Groups    []Group    `json:"groups"`
	Accordion *Accordion `json:"accordion,omitempty"`
}

// Accordion shows a long text as sections the user opens and closes, one row each, on the alternate screen. Every
// text is already laid out for the terminal, colours included; the helper only arranges and scrolls it.
type Accordion struct {
	// Intro is shown above the sections, Outro below them.
	Intro string    `json:"intro,omitempty"`
	Outro string    `json:"outro,omitempty"`
	Items []Section `json:"items"`
	// Open opens every section at the start.
	Open bool `json:"open,omitempty"`
	// Confirm, when set, is offered as a key: pressing ConfirmKey answers AccordionConfirm with Yes, leaving answers No.
	Confirm    string `json:"confirm,omitempty"`
	ConfirmKey string `json:"confirm_key,omitempty"`
	// Accent and Dim are the escapes for the cursor and the key help, so the view matches the caller's theme.
	Accent string `json:"accent,omitempty"`
	Dim    string `json:"dim,omitempty"`
}

// Section is one row of an Accordion: its title and summary on the row, its body under it while it is open.
type Section struct {
	Title   string `json:"title"`
	Summary string `json:"summary,omitempty"`
	Body    string `json:"body,omitempty"`
}

// AccordionConfirm is the answer key of an Accordion's Confirm.
const AccordionConfirm = "confirm"

// Show runs an accordion and reports whether its Confirm key was pressed.
func Show(name string, a Accordion) (bool, error) {
	answers, err := Run(Form{Name: name, Accordion: &a})
	if err != nil {
		return false, err
	}
	return answers.Bool(AccordionConfirm), nil
}

// Answer is what the user gave for one field.
type Answer struct {
	Value  string   `json:"value,omitempty"`
	Values []string `json:"values,omitempty"`
	Yes    bool     `json:"yes,omitempty"`
}

// Answers holds the answers by field key.
type Answers map[string]Answer

// String is the answer to a Select, Input or Text field.
func (a Answers) String(key string) string { return a[key].Value }

// Strings is the answer to a MultiSelect field.
func (a Answers) Strings(key string) []string { return a[key].Values }

// Bool is the answer to a Confirm field.
func (a Answers) Bool(key string) bool { return a[key].Yes }

// Page makes a one-page form.
func Page(name string, fields ...Field) Form {
	return Form{Name: name, Groups: []Group{{Fields: fields}}}
}

// ErrAborted is returned when the user leaves a form without answering (Esc or Ctrl+C).
var ErrAborted = errors.New("cancelled")

// ErrNoHelper is returned when the codeforge-forms program cannot be found.
var ErrNoHelper = errors.New("codeforge-forms is not installed next to codeforge. Built from source? Run `make install` in the codeforge checkout (go install alone leaves it out). Otherwise reinstall codeforge, or set CODEFORGE_FORMS to its path")

// HelperEnv names the environment variable that points at the helper, for installs that keep it elsewhere.
const HelperEnv = "CODEFORGE_FORMS"

// HelperName is the helper's file name.
const HelperName = "codeforge-forms"

// Helper finds the codeforge-forms program: HelperEnv, then next to the running executable, then on PATH.
func Helper() (string, error) {
	if p := os.Getenv(HelperEnv); p != "" {
		return p, nil
	}
	if self, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(self), HelperName)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, nil
		}
	}
	if p, err := exec.LookPath(HelperName); err == nil {
		return p, nil
	}
	return "", ErrNoHelper
}

// The helper reads the form on descriptor 3 and writes the answers on 4 (the first two ExtraFiles); the terminal stays
// on 0, 1 and 2. AbortedExit is how it reports a form the user left.
const AbortedExit = 2

// Run asks the form at the terminal and returns the answers. A form the user leaves is ErrAborted.
func Run(f Form) (Answers, error) {
	bin, err := Helper()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", f.Name, err)
	}
	spec, err := json.Marshal(f)
	if err != nil {
		return nil, fmt.Errorf("%s: encoding the form: %w", f.Name, err)
	}
	specR, specW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", f.Name, err)
	}
	answersR, answersW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", f.Name, err)
	}
	cmd := exec.Command(bin) //nolint:noctx // The form runs for as long as the user takes; a signal ends both processes.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.ExtraFiles = []*os.File{specR, answersW}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%s: starting %s: %w", f.Name, bin, err)
	}
	// The child holds its ends now; closing ours lets it see the end of the form and lets us see the end of the answers.
	_ = specR.Close()
	_ = answersW.Close()
	sent := make(chan error, 1)
	go func() {
		_, err := specW.Write(spec)
		sent <- errors.Join(err, specW.Close())
	}()
	data, readErr := io.ReadAll(answersR)
	_ = answersR.Close()
	if err := cmd.Wait(); err != nil {
		if sendErr := <-sent; sendErr != nil {
			return nil, fmt.Errorf("%s: sending the form: %w", f.Name, sendErr)
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == AbortedExit {
			return nil, fmt.Errorf("%s: %w", f.Name, ErrAborted)
		}
		return nil, fmt.Errorf("%s: %w", f.Name, err)
	}
	if readErr != nil {
		return nil, fmt.Errorf("%s: reading the answers: %w", f.Name, readErr)
	}
	var answers Answers
	if err := json.Unmarshal(data, &answers); err != nil {
		return nil, fmt.Errorf("%s: decoding the answers: %w", f.Name, err)
	}
	return answers, nil
}

// Ask asks one question and returns its answer.
func Ask(name string, field Field) (Answer, error) {
	if field.Key == "" {
		field.Key = "answer"
	}
	answers, err := Run(Page(name, field))
	if err != nil {
		return Answer{}, err
	}
	return answers[field.Key], nil
}

// Choose asks the user to pick one of options; initial is the one offered first.
func Choose(title, description string, options []Option, initial string) (string, error) {
	a, err := Ask(title, Field{Kind: Select, Title: title, Description: description, Options: options, Value: initial})
	return a.Value, err
}

// YesNo asks a yes or no question.
func YesNo(title, description string) (bool, error) {
	a, err := Ask(title, Field{Kind: Confirm, Title: title, Description: description})
	return a.Yes, err
}

// Check names.
const (
	// CheckPaths requires every comma-separated path in the answer to exist.
	CheckPaths = "paths"
)

// ErrUnknownCheck is returned for a Check name this version does not know.
var ErrUnknownCheck = errors.New("unknown check")

// ComplaintError is a validation message shown to the user as it is, next to the field.
type ComplaintError string

func (c ComplaintError) Error() string { return string(c) }

// Validate runs the field's declared checks on an answer; the helper calls it as the user types and before moving on.
func Validate(f Field, value string, values []string) error {
	switch f.Kind {
	case Input, Text:
		if f.Required != "" && strings.TrimSpace(value) == "" {
			return ComplaintError(f.Required)
		}
	case MultiSelect:
		if f.Required != "" && len(values) == 0 {
			return ComplaintError(f.Required)
		}
	case Select, Confirm:
	}
	if f.Check == "" {
		return nil
	}
	return RunCheck(f.Check, value)
}

// RunCheck runs the named check on value.
func RunCheck(name, value string) error {
	if name != CheckPaths {
		return fmt.Errorf("%w: %s", ErrUnknownCheck, name)
	}
	for _, p := range strings.Split(value, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
	}
	return nil
}

// Shown reports whether a group is shown given the answers so far.
func (w *When) Shown(current func(key string) string) bool {
	if w == nil {
		return true
	}
	v := current(w.Key)
	for _, no := range w.IsNot {
		if v == no {
			return false
		}
	}
	if len(w.Is) > 0 && !slices.Contains(w.Is, v) {
		return false
	}
	return w.And.Shown(current)
}
