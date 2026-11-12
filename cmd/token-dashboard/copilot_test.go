package main

import (
	"os"
	"path/filepath"
	"testing"
)

const copilotEventsSample = `{"type":"session.start","data":{"sessionId":"test-copilot-1"},"id":"ev-0","timestamp":"2026-11-05T01:00:00.000Z"}
{"type":"user.message","data":{"role":"user","content":"hello"},"id":"ev-1","timestamp":"2026-11-05T01:00:01.000Z"}
{"type":"session.usage_record","data":{"usage":{"model":"gpt-6-luna","inputTokens":10000,"outputTokens":500,"cacheReadTokens":8000,"cacheWriteTokens":1500,"reasoningTokens":120,"toolCounts":{"bash":2,"view":1},"copilotUsage":{"totalNanoAiu":500000000}}},"id":"ev-2","timestamp":"2026-11-05T01:00:05.000Z"}
{"type":"assistant.message","data":{"role":"assistant","content":"done"},"id":"ev-3","timestamp":"2026-11-05T01:00:06.000Z"}
{"type":"model.response","data":{"quotaSnapshots":{"premium_interactions":{"entitlementRequests":1500,"usedRequests":42,"remainingPercentage":97.2,"resetDate":"2026-12-01T00:00:00Z"}}},"id":"ev-4","timestamp":"2026-11-05T01:00:07.000Z"}
{"type":"session.usage_record","data":{"usage":{"model":"gpt-6-luna","inputTokens":12000,"outputTokens":600,"cacheReadTokens":10000,"cacheWriteTokens":500,"reasoningTokens":150,"toolCounts":{"bash":1},"copilotUsage":{"totalNanoAiu":600000000}}},"id":"ev-5","timestamp":"2026-11-05T01:00:10.000Z"}
`

func withCopilotHome(t *testing.T, sessionID, body string) {
	t.Helper()
	home := t.TempDir()
	sessDir := filepath.Join(home, "session-state", sessionID)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessDir, "events.jsonl"), []byte(body), 0o644); err != nil {
		t.Fatalf("write events: %v", err)
	}
	wsYaml := `id: ` + sessionID + `
cwd: /tmp/test-project
name: Test Copilot Session
updated_at: 2026-11-05T01:00:10.000Z
`
	if err := os.WriteFile(filepath.Join(sessDir, "workspace.yaml"), []byte(wsYaml), 0o644); err != nil {
		t.Fatalf("write workspace: %v", err)
	}
	t.Setenv("COPILOT_HOME", home)
}

func TestReadCopilotSession(t *testing.T) {
	withCopilotHome(t, "test-copilot-1", copilotEventsSample)

	s := tokenStats{Tools: map[string]int{}}
	readCopilotSession("test-copilot-1", &s)

	if s.InputT != 22000 {
		t.Errorf("InputT = %d, want 22000", s.InputT)
	}
	if s.OutputT != 1100 {
		t.Errorf("OutputT = %d, want 1100", s.OutputT)
	}
	if s.CacheR != 18000 {
		t.Errorf("CacheR = %d, want 18000", s.CacheR)
	}
	if s.CacheW != 2000 {
		t.Errorf("CacheW = %d, want 2000", s.CacheW)
	}
	if s.ReasonT != 270 {
		t.Errorf("ReasonT = %d, want 270", s.ReasonT)
	}
	// 500,000,000 + 600,000,000 nanoAiu = 1,100,000,000 / 1e9 = 1.10 AIC
	if s.AICredits < 1.09 || s.AICredits > 1.11 {
		t.Errorf("AICredits = %f, want 1.10", s.AICredits)
	}
	if s.Model != "gpt-6-luna" {
		t.Errorf("Model = %q, want gpt-6-luna", s.Model)
	}
	if s.Provider != "github" {
		t.Errorf("Provider = %q, want github", s.Provider)
	}
	if s.Messages != 2 {
		t.Errorf("Messages = %d, want 2", s.Messages)
	}
	if s.ToolTotal != 4 || s.Tools["bash"] != 3 || s.Tools["view"] != 1 {
		t.Errorf("Tools = %v (total %d), want bash:3, view:1", s.Tools, s.ToolTotal)
	}
	if s.QuotaLimit != 1500 || s.QuotaUsed != 42 || s.QuotaRemPct != 97.2 {
		t.Errorf("Quota = %d/%d (%f%%), want 42/1500 (97.2%%)", s.QuotaUsed, s.QuotaLimit, s.QuotaRemPct)
	}
	if s.Duration.Seconds() != 10 {
		t.Errorf("Duration = %v, want 10s", s.Duration)
	}
}

func TestFindCopilotSession(t *testing.T) {
	withCopilotHome(t, "test-copilot-1", copilotEventsSample)

	// Test title resolution
	p := paneEntry{
		TerminalTitle: "Test Copilot Session - GitHub Copilot",
		Cwd:           "/tmp/test-project",
	}
	got := findCopilotSession(p)
	if got != "test-copilot-1" {
		t.Errorf("findCopilotSession(title) = %q, want test-copilot-1", got)
	}

	// Test cwd fallback resolution
	pNoTitle := paneEntry{
		Cwd: "/tmp/test-project",
	}
	got2 := findCopilotSession(pNoTitle)
	if got2 != "test-copilot-1" {
		t.Errorf("findCopilotSession(cwd) = %q, want test-copilot-1", got2)
	}
}

func TestReadLiveCopilotSession(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	realEvents := filepath.Join(home, ".copilot", "session-state", "c8a05ea3-57e4-4a12-a5be-ee6854860f8e", "events.jsonl")
	if _, err := os.Stat(realEvents); err != nil {
		t.Skip("live copilot session not found, skipping")
	}

	s := tokenStats{Tools: map[string]int{}}
	readCopilotSession("c8a05ea3-57e4-4a12-a5be-ee6854860f8e", &s)

	if s.InputT <= 0 {
		t.Errorf("InputT = %d, expected > 0", s.InputT)
	}
	if s.OutputT <= 0 {
		t.Errorf("OutputT = %d, expected > 0", s.OutputT)
	}
	if s.CacheR <= 0 {
		t.Errorf("CacheR = %d, expected > 0", s.CacheR)
	}
	if s.AICredits < 2.9 || s.AICredits > 3.0 {
		t.Errorf("AICredits = %f, expected ~2.97", s.AICredits)
	}
	if s.Model != "gpt-6-luna" {
		t.Errorf("Model = %q, expected gpt-6-luna", s.Model)
	}
	if s.QuotaLimit <= 0 {
		t.Errorf("QuotaLimit = %d, expected > 0", s.QuotaLimit)
	}
	t.Logf("Live Copilot session: AIC=%.2f, tokens in=%d out=%d cache_r=%d cache_w=%d, msgs=%d, tools=%d, quota=%d/%d (%.1f%%)",
		s.AICredits, s.InputT, s.OutputT, s.CacheR, s.CacheW, s.Messages, s.ToolTotal, s.QuotaUsed, s.QuotaLimit, s.QuotaRemPct)
}
