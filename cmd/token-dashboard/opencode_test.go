package main

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSanitizeOpenCodeSessionID(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"ses_e3f3768f8ffedU5kTlECaUaMM1", "ses_e3f3768f8ffedU5kTlECaUaMM1"},
		{"  ses_12345abc  ", "ses_12345abc"},
		{"/path/to/storage/message/ses_abc123/part.json", "ses_abc123"},
		{"custom_session_id-123", "custom_session_id-123"},
		{"invalid; DROP TABLE session;--", ""},
	}

	for _, tt := range tests {
		got := sanitizeOpenCodeSessionID(tt.input)
		if got != tt.expected {
			t.Errorf("sanitizeOpenCodeSessionID(%q) = %q; want %q", tt.input, got, tt.expected)
		}
	}
}

func TestReadLiveOpenCodeSession(t *testing.T) {
	dbPath := openCodeDBPath()
	if dbPath == "" {
		t.Skip("opencode.db not found on this machine; skipping live test")
	}

	sessionID := "ses_e3f3768f8ffedU5kTlECaUaMM1"
	var s tokenStats
	s.Tools = make(map[string]int)

	readOpenCodeSession(sessionID, &s)

	if s.InputT != 254732 {
		t.Errorf("expected InputT=254732, got %d", s.InputT)
	}
	if s.OutputT != 4219 {
		t.Errorf("expected OutputT=4219, got %d", s.OutputT)
	}
	if s.ReasonT != 1771 {
		t.Errorf("expected ReasonT=1771, got %d", s.ReasonT)
	}
	if s.CacheR != 455061 {
		t.Errorf("expected CacheR=455061, got %d", s.CacheR)
	}
	if s.Model != "coding-free" {
		t.Errorf("expected Model='coding-free', got %q", s.Model)
	}
	if s.Provider != "omni" {
		t.Errorf("expected Provider='omni', got %q", s.Provider)
	}
	if s.Messages != 36 {
		t.Errorf("expected Messages=36, got %d", s.Messages)
	}
	if s.Tools["shell"] != 17 {
		t.Errorf("expected Tools['shell']=17, got %d", s.Tools["shell"])
	}
	if s.Tools["read"] != 3 {
		t.Errorf("expected Tools['read']=3, got %d", s.Tools["read"])
	}
	if s.Tools["edit"] != 2 {
		t.Errorf("expected Tools['edit']=2, got %d", s.Tools["edit"])
	}
	if s.Tools["grep"] != 1 {
		t.Errorf("expected Tools['grep']=1, got %d", s.Tools["grep"])
	}
	if s.Tools["webfetch"] != 1 {
		t.Errorf("expected Tools['webfetch']=1, got %d", s.Tools["webfetch"])
	}
	if s.ToolTotal != 24 {
		t.Errorf("expected ToolTotal=24, got %d", s.ToolTotal)
	}
	if s.Title != "zoxide and fzf shell integration" {
		t.Errorf("expected Title='zoxide and fzf shell integration', got %q", s.Title)
	}
}

func TestReadLegacyOpenCodeSession(t *testing.T) {
	dbPath := openCodeDBPath()
	if dbPath == "" {
		t.Skip("opencode.db not found on this machine; skipping legacy live test")
	}

	sessionID := "ses_fb721e682ffeyopHTi13vOj6s5"
	var s tokenStats
	s.Tools = make(map[string]int)

	readOpenCodeSession(sessionID, &s)

	if s.InputT != 6415371 {
		t.Errorf("expected InputT=6415371, got %d", s.InputT)
	}
	if s.OutputT != 710608 {
		t.Errorf("expected OutputT=710608, got %d", s.OutputT)
	}
	if s.ReasonT != 73755 {
		t.Errorf("expected ReasonT=73755, got %d", s.ReasonT)
	}
	if s.CacheR != 103364032 {
		t.Errorf("expected CacheR=103364032, got %d", s.CacheR)
	}
	if s.Model != "deepseek-v4.1-flash-free" {
		t.Errorf("expected Model='deepseek-v4.1-flash-free', got %q", s.Model)
	}
	if s.Messages != 1272 {
		t.Errorf("expected Messages=1272, got %d", s.Messages)
	}
	if s.Tools["bash"] != 589 {
		t.Errorf("expected Tools['bash']=589, got %d", s.Tools["bash"])
	}
}

func TestFindOpenCodeSession(t *testing.T) {
	dbPath := openCodeDBPath()
	if dbPath == "" {
		t.Skip("opencode.db not found on this machine; skipping find session test")
	}

	p := paneEntry{
		Cwd:   "/Users/Amrinder.Rattanpal/Work-local/Repos/tco-app",
		Agent: "opencode",
	}

	sessionID := findOpenCodeSession(p)
	if sessionID != "ses_e3f3768f8ffedU5kTlECaUaMM1" {
		t.Errorf("expected ses_e3f3768f8ffedU5kTlECaUaMM1, got %q", sessionID)
	}
}

func TestMockOpenCodeDatabase(t *testing.T) {
	sqliteBin := findSQLite3()
	if sqliteBin == "" {
		t.Skip("sqlite3 CLI not available")
	}

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "opencode.db")

	initSQL := `
	CREATE TABLE session_v2 (
		id text PRIMARY KEY,
		title text,
		cost real DEFAULT 0 NOT NULL,
		tokens_input integer DEFAULT 0 NOT NULL,
		tokens_output integer DEFAULT 0 NOT NULL,
		tokens_reasoning integer DEFAULT 0 NOT NULL,
		tokens_cache_read integer DEFAULT 0 NOT NULL,
		tokens_cache_write integer DEFAULT 0 NOT NULL,
		agent text,
		model text,
		directory text,
		time_created integer NOT NULL,
		time_updated integer NOT NULL
	);
	CREATE TABLE session_message (
		id text PRIMARY KEY,
		session_id text NOT NULL,
		type text NOT NULL,
		data text NOT NULL
	);
	INSERT INTO session_v2 (id, title, cost, tokens_input, tokens_output, tokens_reasoning, tokens_cache_read, tokens_cache_write, agent, model, directory, time_created, time_updated)
	VALUES ('ses_mock123', 'Mock Session Title', 0.12, 1000, 200, 50, 500, 10, 'build', '{"id":"claude-3-7-sonnet","providerID":"anthropic"}', '/mock/project', 1000000, 2000000);

	INSERT INTO session_message (id, session_id, type, data) VALUES
	('msg1', 'ses_mock123', 'user', '{"role":"user"}'),
	('msg2', 'ses_mock123', 'assistant', '{"role":"assistant","content":[{"type":"tool","name":"bash"},{"type":"tool","name":"bash"},{"type":"tool","name":"edit"}]}');
	`

	cmd := exec.Command(sqliteBin, dbPath, initSQL)
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to create mock database: %v", err)
	}

	// Set OPENCODE_DB_PATH env var
	t.Setenv("OPENCODE_DB_PATH", dbPath)

	var s tokenStats
	s.Tools = make(map[string]int)

	readOpenCodeSession("ses_mock123", &s)

	if s.InputT != 1000 {
		t.Errorf("expected InputT=1000, got %d", s.InputT)
	}
	if s.OutputT != 200 {
		t.Errorf("expected OutputT=200, got %d", s.OutputT)
	}
	if s.ReasonT != 50 {
		t.Errorf("expected ReasonT=50, got %d", s.ReasonT)
	}
	if s.CacheR != 500 {
		t.Errorf("expected CacheR=500, got %d", s.CacheR)
	}
	if s.CacheW != 10 {
		t.Errorf("expected CacheW=10, got %d", s.CacheW)
	}
	if s.Cost != 0.12 {
		t.Errorf("expected Cost=0.12, got %f", s.Cost)
	}
	if s.Model != "claude-3-7-sonnet" {
		t.Errorf("expected Model='claude-3-7-sonnet', got %q", s.Model)
	}
	if s.Provider != "anthropic" {
		t.Errorf("expected Provider='anthropic', got %q", s.Provider)
	}
	if s.Messages != 1 {
		t.Errorf("expected Messages=1, got %d", s.Messages)
	}
	if s.Tools["bash"] != 2 {
		t.Errorf("expected Tools['bash']=2, got %d", s.Tools["bash"])
	}
	if s.Tools["edit"] != 1 {
		t.Errorf("expected Tools['edit']=1, got %d", s.Tools["edit"])
	}
	if s.ToolTotal != 3 {
		t.Errorf("expected ToolTotal=3, got %d", s.ToolTotal)
	}
	if s.Title != "Mock Session Title" {
		t.Errorf("expected Title='Mock Session Title', got %q", s.Title)
	}

	// Also test findOpenCodeSession
	p := paneEntry{
		Cwd:   "/mock/project",
		Agent: "opencode",
	}
	foundID := findOpenCodeSession(p)
	if foundID != "ses_mock123" {
		t.Errorf("expected findOpenCodeSession to return 'ses_mock123', got %q", foundID)
	}
}
