package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestRolePicksTheReadableHalfPerAppearance(t *testing.T) {
	cases := []struct {
		scheme string
		want   lipgloss.TerminalColor
	}{
		{"", lipgloss.AdaptiveColor{Light: "3", Dark: "11"}},
		{"auto", lipgloss.AdaptiveColor{Light: "3", Dark: "11"}},
		{"nonsense", lipgloss.AdaptiveColor{Light: "3", Dark: "11"}},
		{"light", lipgloss.Color("3")},
		{"dark", lipgloss.Color("11")},
		{"  LIGHT  ", lipgloss.Color("3")},
		{"DARK", lipgloss.Color("11")},
	}

	for _, tc := range cases {
		t.Setenv(schemeEnv, tc.scheme)
		t.Setenv(noColorEnv, "")
		if got := role(3, 11); got != tc.want {
			t.Errorf("%s=%q: role(3, 11) = %#v, want %#v", schemeEnv, tc.scheme, got, tc.want)
		}
	}
}

func TestNoColorDropsColor(t *testing.T) {
	t.Setenv(schemeEnv, "dark")
	t.Setenv(noColorEnv, "1")

	if got := role(3, 11); got != (lipgloss.NoColor{}) {
		t.Errorf("role(3, 11) = %#v, want NoColor", got)
	}
}

// TestPackageHasNoHexColors guards the reason this file exists. A hex value is
// invisible to the ANSI roles above and, being tuned for one background, is
// what made the dashboard unreadable on the other one.
func TestPackageHasNoHexColors(t *testing.T) {
	hex := regexp.MustCompile(`"#[0-9a-fA-F]{6}"`)

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no Go files found")
	}

	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if found := hex.FindString(string(src)); found != "" {
			t.Errorf("%s hard-codes %s; use a role from theme.go instead", name, found)
		}
	}
}
