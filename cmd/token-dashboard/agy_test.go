package main

import (
	"os"
	"path/filepath"
	"testing"
)

const agyTranscriptSample = `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-11-09T16:25:06Z","content":"<USER_REQUEST>\ninstall plugin\n</USER_REQUEST>\n<USER_SETTINGS_CHANGE>\nThe user changed setting ` + "`Model Selection`" + ` from None to Gemini 3.8 Flash (High).\n</USER_SETTINGS_CHANGE>"}
{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-09T16:25:07Z","input_tokens":10000,"cache_read_tokens":20000,"output_tokens":500,"tool_calls":[{"name":"run_command"}]}
{"step_index":2,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-09T16:25:27Z","input_tokens":5000,"cache_read_tokens":10000,"output_tokens":250,"tool_calls":[{"name":"read_file"},{"name":"run_command"}]}
`

func withAgyHome(t *testing.T, sessionID, body string) {
	t.Helper()
	home := t.TempDir()
	logDir := filepath.Join(home, "brain", sessionID, ".system_generated", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "transcript.jsonl"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv("ANTIGRAVITY_APP_DATA_DIR", home)
}

func TestReadAgySession(t *testing.T) {
	withAgyHome(t, "sess-test-1", agyTranscriptSample)

	s := tokenStats{Tools: map[string]int{}}
	readAgySession("sess-test-1", &s)

	if s.InputT != 15000 {
		t.Errorf("InputT = %d, want 15000", s.InputT)
	}
	if s.OutputT != 750 {
		t.Errorf("OutputT = %d, want 750", s.OutputT)
	}
	if s.CacheR != 30000 {
		t.Errorf("CacheR = %d, want 30000", s.CacheR)
	}
	if s.Model != "gemini-3.8-flash" {
		t.Errorf("Model = %q, want gemini-3.8-flash", s.Model)
	}
	if s.Provider != "google" {
		t.Errorf("Provider = %q, want google", s.Provider)
	}
	if s.Messages != 2 {
		t.Errorf("Messages = %d, want 2", s.Messages)
	}
	if s.ToolTotal != 3 || s.Tools["run_command"] != 2 || s.Tools["read_file"] != 1 {
		t.Errorf("tools = %v (total %d), want run_command×2, read_file×1", s.Tools, s.ToolTotal)
	}
	if s.Duration.Seconds() != 21 {
		t.Errorf("Duration = %v, want 21s", s.Duration)
	}

	// Rate for gemini-3.8-flash: in=$0.30, out=$2.50, cacheRead=0.10x ($0.03)
	// (15000*0.30 + 750*2.50 + 30000*0.03) / 1e6
	wantCost := (15000.0*0.30 + 750.0*2.50 + 30000.0*0.03) / 1e6
	if s.Cost != wantCost {
		t.Errorf("Cost = %v, want %v", s.Cost, wantCost)
	}
}

func TestGeminiRates(t *testing.T) {
	cases := []struct {
		model   string
		wantIn  float64
		wantOut float64
	}{
		{"gemini-2.5-pro", 1.25, 10.00},
		{"gemini-3.8-flash", 0.30, 2.50},
		{"gemini-2.0-flash", 0.15, 0.60},
		{"gemini-1.5-flash", 0.075, 0.30},
		{"gemini-flash-lite", 0.075, 0.30},
	}

	for _, c := range cases {
		in, out, ok := geminiRates(c.model)
		if !ok || in != c.wantIn || out != c.wantOut {
			t.Errorf("geminiRates(%q) = (%v, %v, %v), want (%v, %v, true)", c.model, in, out, ok, c.wantIn, c.wantOut)
		}
	}
}

func TestReadLiveAgySession(t *testing.T) {
	// If the real session exists on this machine, test reading it
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	realPath := filepath.Join(home, ".gemini", "antigravity-cli", "brain", "2c6efd82-aea6-4b25-957a-d64111df00d1", ".system_generated", "logs", "transcript.jsonl")
	if _, err := os.Stat(realPath); err != nil {
		t.Skip("live session not found, skipping live test")
	}

	s := tokenStats{Tools: map[string]int{}}
	readAgySession("2c6efd82-aea6-4b25-957a-d64111df00d1", &s)

	if s.InputT <= 0 {
		t.Errorf("Expected InputT > 0, got %d", s.InputT)
	}
	if s.OutputT <= 0 {
		t.Errorf("Expected OutputT > 0, got %d", s.OutputT)
	}
	if s.CacheR <= 0 {
		t.Errorf("Expected CacheR > 0, got %d", s.CacheR)
	}
	if s.Cost <= 0 {
		t.Errorf("Expected Cost > 0, got %f", s.Cost)
	}
	if s.Model != "gemini-3.8-flash" {
		t.Errorf("Model = %q, want gemini-3.8-flash", s.Model)
	}
	if s.Provider != "google" {
		t.Errorf("Provider = %q, want google", s.Provider)
	}
	t.Logf("Live AGY session stats: in=%d, out=%d, cache_r=%d, msgs=%d, tools=%d, cost=$%.4f, model=%s",
		s.InputT, s.OutputT, s.CacheR, s.Messages, s.ToolTotal, s.Cost, s.Model)
}
