//nolint:err113 // dynamic error messages are okay in tests
package src

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/codeliter/codeforge/assets"
)

// paletteKeys are the colours Empryo's theme loader accepts. A key it does not know is dropped, and one a theme leaves out
// falls back to Empryo's violet default, so a Codeforge theme must set every one of them.
var paletteKeys = []string{
	"brand", "brandSecondary", "brandDim", "brandAlt", "error", "success", "warning", "info", "amber",
	"textPrimary", "textSecondary", "textMuted", "textDim", "textFaint", "textSubtle",
	"bgApp", "bgPopup", "bgPopupHighlight", "bgOverlay", "bgPrimary", "bgSecondary", "bgElevated", "bgInput", "bgBanner", "bgBannerError", "bgUser",
	"border", "borderFocused", "borderActive", "borderSlash",
	"diffAddedBg", "diffRemovedBg", "diffAddedSign", "diffRemovedSign",
	"accentUser", "accentAssistant", "accentSystem",
}

var hexColour = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func linear(c float64) float64 {
	if c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

func luminance(t *testing.T, hex string) float64 {
	t.Helper()
	var channel [3]float64
	for i := range channel {
		v, err := strconv.ParseUint(hex[1+2*i:3+2*i], 16, 8)
		require.NoError(t, err)
		channel[i] = linear(float64(v) / 255)
	}
	return 0.2126*channel[0] + 0.7152*channel[1] + 0.0722*channel[2]
}

func contrast(t *testing.T, a, b string) float64 {
	t.Helper()
	la, lb := luminance(t, a), luminance(t, b)
	return (math.Max(la, lb) + 0.05) / (math.Min(la, lb) + 0.05)
}

func redAndBlue(t *testing.T, hex string) (red, blue uint64) {
	t.Helper()
	red, err := strconv.ParseUint(hex[1:3], 16, 8)
	require.NoError(t, err)
	blue, err = strconv.ParseUint(hex[5:7], 16, 8)
	require.NoError(t, err)
	return red, blue
}

func loadTheme(t *testing.T, name string) map[string]string {
	t.Helper()
	raw, err := assets.Profile.ReadFile("themes/" + name + ".json")
	require.NoError(t, err)
	var theme map[string]string
	require.NoError(t, json.Unmarshal(raw, &theme))
	return theme
}

func TestCodeforgeSteelThemes_SetEveryColourEmpryoKnows(t *testing.T) {
	for name, variant := range map[string]string{"codeforge-steel": "dark", "codeforge-steel-light": "light"} {
		theme := loadTheme(t, name)
		require.Equal(t, variant, theme["_variant"], name)
		require.Equal(t, variant, theme["_extends"], name)
		require.NotEmpty(t, theme["_label"], name)
		require.NotEmpty(t, theme["_description"], name)
		for _, key := range paletteKeys {
			require.Regexp(t, hexColour, theme[key], "%s: %s", name, key)
		}
		for key := range theme {
			if !strings.HasPrefix(key, "_") {
				require.True(t, slices.Contains(paletteKeys, key), "%s: %s is not a colour Empryo reads, so it would be ignored", name, key)
			}
		}
	}
}

func TestCodeforgeSteelThemes_AreReadableAndCoolSteel(t *testing.T) {
	for _, name := range []string{"codeforge-steel", "codeforge-steel-light"} {
		theme := loadTheme(t, name)
		for _, surface := range []string{"bgApp", "bgPopup", "bgUser"} {
			bg := theme[surface]
			require.GreaterOrEqual(t, contrast(t, theme["textPrimary"], bg), 10.0, "%s: textPrimary on %s", name, surface)
			require.GreaterOrEqual(t, contrast(t, theme["textSecondary"], bg), 6.5, "%s: textSecondary on %s", name, surface)
			require.GreaterOrEqual(t, contrast(t, theme["textMuted"], bg), 4.0, "%s: textMuted on %s", name, surface)
			for _, ink := range []string{"brand", "brandSecondary", "error", "success", "warning", "info"} {
				require.GreaterOrEqual(t, contrast(t, theme[ink], bg), 4.5, "%s: %s on %s", name, ink, surface)
			}
		}
		// Cool: red never exceeds blue on the brand and the surfaces, so no warm cast creeps in (white is allowed).
		for _, key := range []string{"brand", "brandSecondary", "bgApp", "bgPopup", "bgElevated", "border"} {
			r, b := redAndBlue(t, theme[key])
			require.LessOrEqual(t, r, b, "%s: %s %s is not cool", name, key, theme[key])
		}
		for _, key := range []string{"brand", "brandSecondary", "border"} {
			r, b := redAndBlue(t, theme[key])
			require.Less(t, r, b, "%s: %s %s is grey, not steel blue", name, key, theme[key])
		}
	}
}

func TestSyncAssets_InstallsTheThemesBesideProfiles(t *testing.T) {
	empryo := t.TempDir()
	require.NoError(t, syncAssets(empryo, Config{}))
	for _, name := range []string{"codeforge-steel.json", "codeforge-steel-light.json"} {
		got, err := os.ReadFile(filepath.Join(empryo, "themes", name))
		require.NoError(t, err)
		want, err := assets.Profile.ReadFile("themes/" + name)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
	require.FileExists(t, filepath.Join(empryo, "agents", "codeforge-chat.md"))
}
