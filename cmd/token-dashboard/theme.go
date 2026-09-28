package main

import (
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ── Palette ─────────────────────────────────────────────────────────────────
//
// Every colour this dashboard draws is an ANSI palette role, never a hex value.
//
// The dashboard runs inside a Herdr pane, and a Herdr pane is a real terminal.
// Herdr answers OSC 10/11, OSC 4;N;? and CSI ?996n for that pane using the
// values it read from the host terminal when you attached, and it passes an
// indexed colour straight through to the host rather than resolving it itself
// (herdr src/pane/terminal.rs, ghostty_cell_color -> ratatui Color::Indexed).
// So index 3 costs nothing here and resolves to the yellow the user already
// sees in every other terminal program on the machine — and it is the right
// yellow in light *and* dark, because the palette is exactly what they tuned
// for each appearance.
//
// Hard-coding Tailwind hexes, which is what this plugin used to do, is what
// made the dashboard unreadable on a light terminal: #e5e7eb — the "white" used
// for every value in the detail cards — is 1.2:1 on a #fef8ec background, and
// the rest of the palette lands between 1.4:1 and 2.6:1.
//
// Roles come in two halves: 1-6 are the normal variants, 9-14 the bright ones,
// and a theme puts the readable one of each pair against its own background. So
// an AdaptiveColor carrying both halves follows the terminal without shipping a
// colour table.
//
// What this deliberately does *not* do is match Herdr's own chrome theme
// (config.toml [theme]). Pane content has never inherited it, and Herdr does
// not expose its resolved palette to plugin processes yet — see
// herdrdev/herdr discussion #1796. Until it does, the terminal is the only
// palette a pane can honestly draw with.

// schemeEnv pins the appearance and skips detection. Needed for terminals Herdr
// cannot read an appearance from (a detached server answering the scheme and
// colour queries with its own stale state) and for tests. "auto" is the default.
const schemeEnv = "HERDR_TOKEN_DASHBOARD_SCHEME"

// noColorEnv is the standard opt-out (https://no-color.org).
const noColorEnv = "NO_COLOR"

var (
	cyan    = role(6, 14)
	green   = role(2, 10)
	yellow  = role(3, 11)
	red     = role(1, 9)
	magenta = role(5, 13)
	blue    = role(4, 12)

	// One muted role. The palette has no second, quieter grey, and faking one
	// with a hex value is what got us here. ANSI 8 is the conventional
	// "comments and secondary chrome" slot in both appearances.
	dim = role(8, 8)

	// The terminal's own default foreground, which is by definition readable
	// against its default background. Leaving the foreground unset is how a
	// terminal program asks for it.
	text = lipgloss.NoColor{}

	// Text on the cyan title chip. ANSI 0 is the theme's near-black, which is
	// the readable half of a bright cyan chip in either appearance.
	titleFg = role(0, 0)
)

// role builds a colour from its normal and bright ANSI indices: the normal
// variant is the readable one on a light background, the bright one on a dark
// background. A pinned HERDR_TOKEN_DASHBOARD_SCHEME collapses it to one half;
// NO_COLOR drops colour entirely.
func role(normal, bright uint8) lipgloss.TerminalColor {
	if os.Getenv(noColorEnv) != "" {
		return lipgloss.NoColor{}
	}
	switch forcedScheme() {
	case "light":
		return lipgloss.Color(index(normal))
	case "dark":
		return lipgloss.Color(index(bright))
	default:
		return lipgloss.AdaptiveColor{Light: index(normal), Dark: index(bright)}
	}
}

// forcedScheme returns the pinned appearance, or "" to detect it.
func forcedScheme() string {
	switch scheme := strings.ToLower(strings.TrimSpace(os.Getenv(schemeEnv))); scheme {
	case "light", "dark":
		return scheme
	default:
		return ""
	}
}

func index(ansi uint8) string {
	return strconv.Itoa(int(ansi))
}
