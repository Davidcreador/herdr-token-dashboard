package main

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func press(m model, text string) model {
	k := tea.KeyPressMsg{Text: text, Code: []rune(text)[0]}
	next, _ := m.Update(k)
	return next.(model)
}

// TestViewScrolls: the body is clipped to the terminal height and the keys
// move a window over it, clamped at both ends.
func TestViewScrolls(t *testing.T) {
	m := model{width: 120, height: 20, prevStatus: map[string]string{}}
	for i := 0; i < 30; i++ {
		m.stats = append(m.stats, tokenStats{PaneID: fmt.Sprintf("w%d:p1", i), Agent: "claude",
			Cwd: fmt.Sprintf("/p/proj%02d", i), Tools: map[string]int{}})
	}

	lines := func(m model) []string { return strings.Split(m.View().Content, "\n") }
	if got := len(lines(m)); got != m.height {
		t.Fatalf("view has %d lines, want exactly the terminal height %d", got, m.height)
	}
	if !strings.Contains(m.View().Content, "lines 1–") {
		t.Fatalf("help line should show the scroll position when the body overflows")
	}

	m = press(m, "j")
	if m.offset != 1 {
		t.Fatalf("offset after j = %d, want 1", m.offset)
	}
	m = press(m, "k")
	m = press(m, "k")
	if m.offset != 0 {
		t.Fatalf("offset after k,k = %d, want 0 (clamped)", m.offset)
	}
	m = press(m, "G")
	if !strings.Contains(m.View().Content, "proj29") {
		t.Fatalf("G should scroll to the last session card")
	}
	if strings.Contains(m.View().Content, "proj00 ") && strings.Contains(m.View().Content, "SESSION") {
		t.Fatalf("G should have scrolled the table header out of view")
	}
}
