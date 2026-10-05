package main

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestProjectName(t *testing.T) {
	for in, want := range map[string]string{
		"/home/u/projects/crm/.claude/worktrees/order-quick-action": "crm/order-quick-action",
		"/home/u/.herdr/worktrees/translation/hub-session":          "translation/hub-session",
		"/home/u/projects/backlog/":                                 "backlog",
		"":                                                          "",
	} {
		if got := projectName(in); got != want {
			t.Errorf("projectName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPaneDisplay(t *testing.T) {
	cases := []struct {
		name string
		s    tokenStats
		want string
	}{
		{"named tab wins", tokenStats{TabLabel: "review", Cwd: "/p/app", SessTitle: "Fix login"}, "review"},
		{"default numeric tab -> project · title", tokenStats{TabLabel: "1", Cwd: "/p/app", SessTitle: "Fix login"}, "app · Fix login"},
		{"no title -> project", tokenStats{TabLabel: "1", Cwd: "/p/app"}, "app"},
		{"nothing -> pane id", tokenStats{PaneID: "wQ:p1"}, "wQ:p1"},
	}
	for _, c := range cases {
		if got := paneDisplay(c.s); got != c.want {
			t.Errorf("%s: paneDisplay = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestTruncRuneSafe(t *testing.T) {
	got := trunc("app · Récupération des données", 12)
	if lipgloss.Width(got) > 12 || got[len(got)-len("…"):] != "…" {
		t.Errorf("trunc = %q (width %d), want ≤12 cells ending in …", got, lipgloss.Width(got))
	}
	for _, r := range got {
		if r == '�' {
			t.Fatalf("trunc split a multi-byte rune: %q", got)
		}
	}
}
