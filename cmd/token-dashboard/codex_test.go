package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Trimmed from a real `codex exec` rollout (Codex CLI 0.147.0): session_meta,
// turn_context, one tool call, two agent messages, and two cumulative
// token_count events.
const codexRollout = `{"timestamp":"2026-08-11T04:51:33.000Z","type":"session_meta","payload":{"session_id":"sess-1","id":"sess-1","cwd":"/work/repo"}}
{"timestamp":"2026-08-11T04:51:34.000Z","type":"turn_context","payload":{"model":"gpt-5.6-sol"}}
{"timestamp":"2026-08-11T04:51:35.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":18599,"cached_input_tokens":11008,"cache_write_input_tokens":0,"output_tokens":5,"reasoning_output_tokens":0,"total_tokens":18604},"model_context_window":258400}}}
{"timestamp":"2026-08-11T04:51:36.000Z","type":"event_msg","payload":{"type":"agent_message"}}
{"timestamp":"2026-08-11T04:51:40.000Z","type":"response_item","payload":{"type":"custom_tool_call","name":"exec"}}
{"timestamp":"2026-08-11T04:51:45.000Z","type":"event_msg","payload":{"type":"agent_message"}}
{"timestamp":"2026-08-11T04:52:03.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":38218,"cached_input_tokens":22016,"cache_write_input_tokens":0,"output_tokens":138,"reasoning_output_tokens":0,"total_tokens":38356},"model_context_window":258400}}}
`

// withCodexHome points CODEX_HOME at a temp dir holding one rollout.
func withCodexHome(t *testing.T, name, body string) {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, "sessions", "2026", "08", "10")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv("CODEX_HOME", home)
}

func TestReadCodexSession(t *testing.T) {
	withCodexHome(t, "rollout-2026-08-10T21-51-33-sess-1.jsonl", codexRollout)

	s := tokenStats{Tools: map[string]int{}}
	readCodexSession("sess-1", &s)

	// Cumulative totals: the LAST token_count wins rather than summing.
	// input_tokens is inclusive of cached, so IN is reported uncached:
	// 38218 - 22016 - 0 = 16202.
	if s.InputT != 16202 {
		t.Errorf("InputT = %d, want 16202", s.InputT)
	}
	if s.OutputT != 138 {
		t.Errorf("OutputT = %d, want 138", s.OutputT)
	}
	if s.CacheR != 22016 {
		t.Errorf("CacheR = %d, want 22016", s.CacheR)
	}
	if s.Model != "gpt-5.6-sol" {
		t.Errorf("Model = %q, want gpt-5.6-sol", s.Model)
	}
	if s.Provider != "openai" {
		t.Errorf("Provider = %q, want openai", s.Provider)
	}
	if s.Messages != 2 {
		t.Errorf("Messages = %d, want 2", s.Messages)
	}
	if s.ToolTotal != 1 || s.Tools["exec"] != 1 {
		t.Errorf("tools = %v (total %d), want exec×1", s.Tools, s.ToolTotal)
	}
	if s.Cwd != "/work/repo" {
		t.Errorf("Cwd = %q, want /work/repo", s.Cwd)
	}
	if s.Duration.Seconds() != 30 {
		t.Errorf("Duration = %v, want 30s", s.Duration)
	}
	// gpt-5.6-sol at $5/$30 per MTok, cache read 0.1x in:
	// (16202*5 + 138*30 + 22016*0.5 + 0) / 1e6
	want := (16202*5.0 + 138*30.0 + 22016*0.5) / 1e6
	if s.Cost != want {
		t.Errorf("Cost = %v, want %v", s.Cost, want)
	}
}

func TestReadCodexSessionResolvesBySessionMeta(t *testing.T) {
	// Filename carries no session id, so the session_meta fallback must find it.
	withCodexHome(t, "rollout-2026-08-10T21-51-33-other.jsonl", codexRollout)

	s := tokenStats{Tools: map[string]int{}}
	readCodexSession("sess-1", &s)

	if s.Model != "gpt-5.6-sol" {
		t.Errorf("fallback lookup failed: Model = %q", s.Model)
	}
}

func TestReadCodexSessionMissingRollout(t *testing.T) {
	withCodexHome(t, "rollout-2026-08-10T21-51-33-sess-1.jsonl", codexRollout)

	s := tokenStats{Tools: map[string]int{}}
	readCodexSession("nope", &s)

	if s.InputT != 0 || s.Model != "" || s.Messages != 0 {
		t.Errorf("expected zero stats for unknown session, got %+v", s)
	}
}

func TestOpenAIRates(t *testing.T) {
	cases := []struct {
		model  string
		wantIn float64
		wantOK bool
	}{
		// Longest match must win: every id below also contains "gpt-5".
		{"gpt-5.6-sol", 5, true},
		{"gpt-5.6-luna", 0.2, true},
		{"gpt-5.3-codex", 1.75, true},
		{"gpt-5.4-mini", 0.75, true},
		{"gpt-5.4", 2.5, true},
		{"gpt-5", 1.25, true},
		// Deliberately absent: gpt-4.x caches at 0.25x/0.5x, not 0.1x, so
		// blendedCost would misprice it. No rate is better than a wrong one.
		{"gpt-4.1", 0, false},
		{"gpt-4o", 0, false},
		{"some-future-model", 0, false},
		{"", 0, false},
	}
	for _, tc := range cases {
		in, _, ok := openaiRates(tc.model)
		if ok != tc.wantOK || in != tc.wantIn {
			t.Errorf("openaiRates(%q) = (in=%v, ok=%v), want (in=%v, ok=%v)",
				tc.model, in, ok, tc.wantIn, tc.wantOK)
		}
	}
}
