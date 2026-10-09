package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func histLine(ts, id, req string, in, out int) string {
	return fmt.Sprintf(`{"type":"assistant","cwd":"/home/u/projects/app","timestamp":%q,"requestId":%q,"message":{"id":%q,"model":"claude-opus-5","usage":{"input_tokens":%d,"output_tokens":%d,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}`,
		ts, req, id, in, out)
}

// TestHistoryIncrementalScan: a rescan reads only appended lines, dedupes a
// streamed duplicate, ignores a partial last line until it is complete, and
// counts subagent transcripts toward the same project.
func TestHistoryIncrementalScan(t *testing.T) {
	root := t.TempDir()
	withClaudeProjectsRoot(t, root)
	h := &history{files: map[string]*histFile{}, names: map[string]string{}}

	now := time.Now()
	ts := now.UTC().Format(time.RFC3339Nano)
	dir := filepath.Join(root, mungeClaudePath("/home/u/projects/app"))
	if err := os.MkdirAll(filepath.Join(dir, testSessionID, "subagents"), 0755); err != nil {
		t.Fatal(err)
	}
	mainPath := filepath.Join(dir, testSessionID+".jsonl")
	write := func(p, s string, appendTo bool) {
		flag := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
		if appendTo {
			flag = os.O_CREATE | os.O_WRONLY | os.O_APPEND
		}
		f, err := os.OpenFile(p, flag, 0644)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString(s)
		f.Close()
	}
	write(mainPath, histLine(ts, "m1", "r1", 1000, 0)+"\n"+histLine(ts, "m1", "r1", 1000, 0)+"\n", false)
	write(filepath.Join(dir, testSessionID, "subagents", "agent-x.jsonl"), histLine(ts, "s1", "r9", 2000, 0)+"\n", false)

	h.scan()
	unit := claudeCost("claude-opus-5", 1000, 0, 0, 0)
	got, _ := func() (float64, bool) { h.scanned = now; return h.todayFor(filepath.Base(dir), now) }()
	if math.Abs(got-3*unit) > 1e-9 {
		t.Fatalf("today after first scan = %v, want %v (main deduped + subagent)", got, 3*unit)
	}

	// Append one complete line and one partial line.
	partial := histLine(ts, "m3", "r3", 1000, 0)
	write(mainPath, histLine(ts, "m2", "r2", 1000, 0)+"\n"+partial[:40], true)
	h.scan()
	if got, _ := h.todayFor(filepath.Base(dir), now); math.Abs(got-4*unit) > 1e-9 {
		t.Fatalf("today after append = %v, want %v (partial line not yet counted)", got, 4*unit)
	}
	write(mainPath, partial[40:]+"\n", true)
	h.scan()
	if got, _ := h.todayFor(filepath.Base(dir), now); math.Abs(got-5*unit) > 1e-9 {
		t.Fatalf("today after completing the line = %v, want %v", got, 5*unit)
	}

	rows, daily := h.summary(now, 3)
	if len(rows) != 1 || rows[0].Name != "app" || rows[0].Sessions != 1 {
		t.Fatalf("rows = %+v, want one row named app with 1 session", rows)
	}
	if math.Abs(rows[0].Sub7-2*unit) > 1e-9 || math.Abs(daily[0]-5*unit) > 1e-9 {
		t.Fatalf("Sub7 = %v daily[0] = %v, want %v and %v", rows[0].Sub7, daily[0], 2*unit, 5*unit)
	}
}

// TestHistoryRealScan times a scan of the real ~/.claude/projects. Opt-in:
// HISTORY_REAL_SCAN=1 go test -run RealScan -v ./...
func TestHistoryRealScan(t *testing.T) {
	if os.Getenv("HISTORY_REAL_SCAN") == "" {
		t.Skip("set HISTORY_REAL_SCAN=1")
	}
	h := &history{files: map[string]*histFile{}, names: map[string]string{}}
	t0 := time.Now()
	h.scan()
	first := time.Since(t0)
	t0 = time.Now()
	h.scan()
	second := time.Since(t0)
	rows, daily := h.summary(time.Now(), 3)
	t.Logf("files=%d first scan %v, rescan %v; today $%.2f yesterday $%.2f", len(h.files), first, second, daily[0], daily[1])
	for i, r := range rows {
		if i == 8 {
			break
		}
		t.Logf("  %-50s today $%7.2f  7d $%8.2f  all $%9.2f  sub7 %3.0f%%  sessions %d",
			r.Name, r.Today, r.D7, r.All, 100*r.Sub7/math.Max(r.D7, 1e-9), r.Sessions)
	}
}
