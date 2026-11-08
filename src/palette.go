package src

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/alecthomas/chroma/v2"

	"github.com/codeliter/codeforge/assets"
)

const ansiReset = "\x1b[0m"

// palette is the Codeforge Steel theme as terminal colours, for what codeforge prints itself (the gate report, the
// resume hint), so its output looks like the TUI it starts.
type palette struct {
	accent string // bold brand: commands, file paths, the finding's line
	dim    string // muted text: titles, gutters, notes
	bold   string
	inline string // `inline code` in a review
	// Conventional Comment labels: blocking issues, suggestions, questions, praise.
	issue, suggestion, question, praise string
	// Backgrounds of a code card: its body, its title row and the finding's line.
	card, cardTitle, cardLine string
	// Added and removed lines of a diff.
	added, removed string
	code           *chroma.Style
}

func (palette) paint(code, s string) string { return code + s + ansiReset }

var (
	palettes   = map[string]palette{}
	palettesMu sync.Mutex
)

// paletteFor returns the Steel palette for "dark" or "light", read from the theme files codeforge ships.
func paletteFor(appearance string) palette {
	name := steelFor(appearance)
	palettesMu.Lock()
	defer palettesMu.Unlock()
	if p, ok := palettes[name]; ok {
		return p
	}
	p := buildPalette(themeColors(name))
	palettes[name] = p
	return p
}

// themeColors reads a shipped theme's colours by key. A theme that cannot be read gives none, and every colour then
// falls back to the terminal's own.
func themeColors(name string) map[string]string {
	data, err := assets.Profile.ReadFile("themes/" + name + ".json")
	if err != nil {
		return nil
	}
	var raw map[string]any
	if json.Unmarshal(data, &raw) != nil {
		return nil
	}
	out := map[string]string{}
	for k, v := range raw {
		if s, ok := v.(string); ok && strings.HasPrefix(s, "#") && len(s) == len("#rrggbb") {
			out[k] = s
		}
	}
	return out
}

// The SGR codes that set a true colour for the text and for the background.
const (
	sgrForeground = 38
	sgrBackground = 48
)

// fg is the true-colour escape for a #rrggbb colour, or "" when it is missing or malformed.
func fg(hex string) string { return trueColour(sgrForeground, hex) }

// bg is fg for the background.
func bg(hex string) string { return trueColour(sgrBackground, hex) }

func trueColour(layer int, hex string) string {
	if len(hex) != len("#rrggbb") {
		return ""
	}
	v, err := strconv.ParseUint(hex[1:], 16, 32)
	if err != nil {
		return ""
	}
	const red, green = 16, 8 // bit offsets of the channels in 0xrrggbb
	return fmt.Sprintf("\x1b[%d;2;%d;%d;%dm", layer, byte(v>>red), byte(v>>green), byte(v))
}

func buildPalette(c map[string]string) palette {
	pick := func(keys ...string) string {
		for _, k := range keys {
			if c[k] != "" {
				return c[k]
			}
		}
		return ""
	}
	bold := "\x1b[1m"
	p := palette{
		accent:     bold + fg(pick("brand")),
		dim:        fg(pick("textMuted")),
		bold:       bold,
		inline:     fg(pick("brandSecondary", "brand")),
		issue:      bold + fg(pick("error")),
		suggestion: bold + fg(pick("info", "brand")),
		question:   bold + fg(pick("warning")),
		praise:     bold + fg(pick("success")),
		card:       bg(pick("bgElevated", "bgPopup")),
		cardTitle:  bg(pick("bgBanner", "bgPopupHighlight")),
		cardLine:   bg(pick("brandDim", "bgPopupHighlight")),
		added:      fg(pick("diffAddedSign", "success")),
		removed:    fg(pick("diffRemovedSign", "error")),
	}
	if p.dim == "" {
		p.dim = "\x1b[2m"
	}
	entries := chroma.StyleEntries{}
	add := func(t chroma.TokenType, color string, extra ...string) {
		if color != "" {
			entries[t] = strings.Join(append([]string{color}, extra...), " ")
		}
	}
	add(chroma.Text, pick("textPrimary"))
	add(chroma.Keyword, pick("brand"), "bold")
	add(chroma.KeywordType, pick("brandSecondary"))
	add(chroma.KeywordConstant, pick("warning"))
	add(chroma.NameBuiltin, pick("brandSecondary"))
	add(chroma.NameFunction, pick("info"))
	add(chroma.NameClass, pick("brandAlt", "brand"))
	add(chroma.NameTag, pick("brandAlt", "brand"))
	add(chroma.NameAttribute, pick("info"))
	add(chroma.NameConstant, pick("warning"))
	add(chroma.LiteralString, pick("success"))
	add(chroma.LiteralNumber, pick("amber"))
	add(chroma.Comment, pick("textDim"), "italic")
	add(chroma.Operator, pick("textSecondary"))
	add(chroma.Punctuation, pick("textSecondary"))
	add(chroma.GenericInserted, pick("success"))
	add(chroma.GenericDeleted, pick("error"))
	add(chroma.Error, pick("error"))
	if style, err := chroma.NewStyle("codeforge-steel", entries); err == nil {
		p.code = style
	}
	return p
}
