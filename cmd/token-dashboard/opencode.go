package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const opencodeServer = "http://127.0.0.1:4096"

var (
	reOpenCodeSession = regexp.MustCompile(`ses_[a-zA-Z0-9]+`)
	reSafeSQLID       = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
)

// OpenCode server API response types (legacy).
type ocSessionResponse struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	Cost      float64 `json:"cost"`
	Agent     string  `json:"agent"`
	Directory string  `json:"directory"`
	Model     struct {
		ID         string `json:"id"`
		ProviderID string `json:"providerID"`
		Variant    string `json:"variant"`
	} `json:"model"`
	Tokens struct {
		Input     int `json:"input"`
		Output    int `json:"output"`
		Reasoning int `json:"reasoning"`
		Cache     struct {
			Read  int `json:"read"`
			Write int `json:"write"`
		} `json:"cache"`
	} `json:"tokens"`
	Time struct {
		Created int64 `json:"created"`
		Updated int64 `json:"updated"`
	} `json:"time"`
}

type ocMessageListResponse []struct {
	Info struct {
		Role string `json:"role"`
	} `json:"info"`
	Parts []struct {
		Type string `json:"type"`
		Tool string `json:"tool"`
	} `json:"parts"`
}

// Database query response types.
type ocDBSession struct {
	ID               string  `json:"id"`
	Title            string  `json:"title"`
	Cost             float64 `json:"cost"`
	TokensInput      int     `json:"tokens_input"`
	TokensOutput     int     `json:"tokens_output"`
	TokensReasoning  int     `json:"tokens_reasoning"`
	TokensCacheRead  int     `json:"tokens_cache_read"`
	TokensCacheWrite int     `json:"tokens_cache_write"`
	Agent            string  `json:"agent"`
	Model            string  `json:"model"`
	TimeCreated      int64   `json:"time_created"`
	TimeUpdated      int64   `json:"time_updated"`
}

type ocDBMsgCount struct {
	AssistantMessages int `json:"assistant_messages"`
}

type ocDBToolCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// openCodeDBPath locates the OpenCode SQLite database file.
func openCodeDBPath() string {
	if custom := os.Getenv("OPENCODE_DB_PATH"); custom != "" {
		if _, err := os.Stat(custom); err == nil {
			return custom
		}
	}
	if custom := os.Getenv("OPENCODE_DATA_DIR"); custom != "" {
		p := filepath.Join(custom, "opencode.db")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		p := filepath.Join(xdg, "opencode", "opencode.db")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	// Default: ~/.local/share/opencode/opencode.db
	p1 := filepath.Join(home, ".local", "share", "opencode", "opencode.db")
	if _, err := os.Stat(p1); err == nil {
		return p1
	}
	// macOS Library fallback
	p2 := filepath.Join(home, "Library", "Application Support", "opencode", "opencode.db")
	if _, err := os.Stat(p2); err == nil {
		return p2
	}
	return ""
}

// findSQLite3 finds an available sqlite3 CLI binary.
func findSQLite3() string {
	if p, err := exec.LookPath("sqlite3"); err == nil {
		return p
	}
	for _, candidate := range []string{
		"/usr/bin/sqlite3",
		"/usr/local/bin/sqlite3",
		"/opt/homebrew/bin/sqlite3",
	} {
		if fi, err := os.Stat(candidate); err == nil && !fi.IsDir() {
			return candidate
		}
	}
	return ""
}

// sanitizeSessionID validates and extracts a clean session ID.
func sanitizeOpenCodeSessionID(raw string) string {
	raw = strings.TrimSpace(raw)
	if match := reOpenCodeSession.FindString(raw); match != "" {
		return match
	}
	if reSafeSQLID.MatchString(raw) {
		return raw
	}
	return ""
}

// readOpenCodeSession extracts stats for an OpenCode session.
// It prioritizes reading from the local SQLite database (~/.local/share/opencode/opencode.db),
// and falls back to HTTP server API (if running) and legacy disk storage.
func readOpenCodeSession(sessionID string, s *tokenStats) {
	if sessionID == "" {
		return
	}

	// 1. Try local SQLite database (modern OpenCode CLI default)
	if err := readOpenCodeSQLite(sessionID, s); err == nil && (s.InputT > 0 || s.OutputT > 0 || s.Messages > 0 || s.Model != "") {
		return
	}

	// 2. Fall back to live HTTP server (legacy / server mode on port 4096)
	readOpenCodeLive(sessionID, s)
	if s.InputT > 0 || s.OutputT > 0 || s.Messages > 0 || s.Model != "" {
		return
	}

	// 3. Fall back to legacy disk message directory
	readOpenCodeDisk(sessionID, s)
}

// readOpenCodeSQLite queries the local OpenCode SQLite database for session metadata,
// assistant message counts, and tool usage breakdown.
func readOpenCodeSQLite(sessionID string, s *tokenStats) error {
	cleanID := sanitizeOpenCodeSessionID(sessionID)
	if cleanID == "" {
		return fmt.Errorf("invalid opencode session id: %s", sessionID)
	}

	dbPath := openCodeDBPath()
	if dbPath == "" {
		return fmt.Errorf("opencode database not found")
	}

	sqliteBin := findSQLite3()
	if sqliteBin == "" {
		return fmt.Errorf("sqlite3 binary not found")
	}

	// Attempt modern schema (session_v2 + session_message)
	modernSQL := fmt.Sprintf(
		"SELECT id, title, cost, tokens_input, tokens_output, tokens_reasoning, tokens_cache_read, tokens_cache_write, agent, model, time_created, time_updated FROM session_v2 WHERE id = '%s';\n"+
			"SELECT count(*) as assistant_messages FROM session_message WHERE session_id = '%s' AND type = 'assistant';\n"+
			"SELECT json_extract(value, '$.name') as name, count(*) as count FROM session_message, json_each(json_extract(data, '$.content')) WHERE session_id = '%s' AND json_extract(value, '$.type') = 'tool' GROUP BY 1;",
		cleanID, cleanID, cleanID,
	)

	sessions, msgCount, tools, err := execOpenCodeSQL(sqliteBin, dbPath, modernSQL)
	if err != nil || len(sessions) == 0 {
		// Fall back to legacy schema (session + message + part)
		legacySQL := fmt.Sprintf(
			"SELECT id, title, cost, tokens_input, tokens_output, tokens_reasoning, tokens_cache_read, tokens_cache_write, agent, model, time_created, time_updated FROM session WHERE id = '%s';\n"+
				"SELECT count(*) as assistant_messages FROM message WHERE session_id = '%s' AND json_extract(data, '$.role') = 'assistant';\n"+
				"SELECT json_extract(data, '$.tool') as name, count(*) as count FROM part WHERE session_id = '%s' AND json_extract(data, '$.type') = 'tool' GROUP BY 1;",
			cleanID, cleanID, cleanID,
		)
		sessions, msgCount, tools, err = execOpenCodeSQL(sqliteBin, dbPath, legacySQL)
		if err != nil || len(sessions) == 0 {
			return fmt.Errorf("session not found in opencode database")
		}
	}

	sess := sessions[0]
	s.Cost = sess.Cost
	s.InputT = sess.TokensInput
	s.OutputT = sess.TokensOutput
	s.ReasonT = sess.TokensReasoning
	s.CacheR = sess.TokensCacheRead
	s.CacheW = sess.TokensCacheWrite
	s.Mode = sess.Agent
	if sess.Title != "" {
		s.Title = sess.Title
	}

	if sess.TimeCreated > 0 {
		s.Started = time.UnixMilli(sess.TimeCreated)
	}
	if sess.TimeUpdated > 0 {
		s.LastAct = time.UnixMilli(sess.TimeUpdated)
	}
	if !s.Started.IsZero() && !s.LastAct.IsZero() {
		s.Duration = s.LastAct.Sub(s.Started)
	}

	parseOpenCodeModel(sess.Model, s)

	s.Messages = msgCount

	if s.Tools == nil {
		s.Tools = make(map[string]int)
	}
	for name, count := range tools {
		if name != "" && count > 0 {
			s.Tools[name] = count
			s.ToolTotal += count
		}
	}

	return nil
}

// execOpenCodeSQL runs sqlite3 -json and decodes the multi-statement JSON results.
func execOpenCodeSQL(sqliteBin, dbPath, sql string) ([]ocDBSession, int, map[string]int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, sqliteBin, "-json", dbPath, sql)
	out, err := cmd.Output()
	if err != nil {
		return nil, 0, nil, err
	}

	trimmed := bytes.TrimSpace(out)
	if len(trimmed) == 0 {
		return nil, 0, nil, nil
	}

	dec := json.NewDecoder(bytes.NewReader(trimmed))

	var sessions []ocDBSession
	if err := dec.Decode(&sessions); err != nil {
		return nil, 0, nil, err
	}

	var msgCount int
	var msgCounts []ocDBMsgCount
	if err := dec.Decode(&msgCounts); err == nil && len(msgCounts) > 0 {
		msgCount = msgCounts[0].AssistantMessages
	}

	toolMap := make(map[string]int)
	var toolCounts []ocDBToolCount
	if err := dec.Decode(&toolCounts); err == nil {
		for _, tc := range toolCounts {
			if tc.Name != "" && tc.Count > 0 {
				toolMap[tc.Name] = tc.Count
			}
		}
	}

	return sessions, msgCount, toolMap, nil
}

// findOpenCodeSession locates the most recent session matching the given pane's working directory.
func findOpenCodeSession(p paneEntry) string {
	if p.Cwd == "" {
		return ""
	}
	dbPath := openCodeDBPath()
	if dbPath == "" {
		return ""
	}
	sqliteBin := findSQLite3()
	if sqliteBin == "" {
		return ""
	}

	escapedCwd := strings.ReplaceAll(p.Cwd, "'", "''")

	// Try session_v2 first
	sqlV2 := fmt.Sprintf("SELECT id FROM session_v2 WHERE directory = '%s' ORDER BY time_updated DESC LIMIT 1;", escapedCwd)
	if id := querySingleID(sqliteBin, dbPath, sqlV2); id != "" {
		return id
	}

	// Fall back to legacy session
	sqlLegacy := fmt.Sprintf("SELECT id FROM session WHERE directory = '%s' ORDER BY time_updated DESC LIMIT 1;", escapedCwd)
	return querySingleID(sqliteBin, dbPath, sqlLegacy)
}

func querySingleID(sqliteBin, dbPath, sql string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, sqliteBin, "-json", dbPath, sql)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}

	var results []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &results); err == nil && len(results) > 0 {
		return results[0].ID
	}
	return ""
}

// parseOpenCodeModel parses the raw model JSON or string into Model and Provider.
func parseOpenCodeModel(rawModel string, s *tokenStats) {
	if rawModel == "" {
		return
	}
	var m struct {
		ID         string `json:"id"`
		ProviderID string `json:"providerID"`
	}
	if err := json.Unmarshal([]byte(rawModel), &m); err == nil && m.ID != "" {
		s.Model = m.ID
		if m.ProviderID != "" {
			s.Provider = m.ProviderID
		}
		return
	}
	s.Model = rawModel
}

// readOpenCodeLive queries the legacy OpenCode server API for live session data.
func readOpenCodeLive(sessionID string, s *tokenStats) {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Get(opencodeServer + "/session/" + sessionID)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}

	var sess ocSessionResponse
	if json.Unmarshal(body, &sess) != nil {
		return
	}

	s.Cost = sess.Cost
	s.InputT = sess.Tokens.Input
	s.OutputT = sess.Tokens.Output
	s.ReasonT = sess.Tokens.Reasoning
	s.CacheR = sess.Tokens.Cache.Read
	s.CacheW = sess.Tokens.Cache.Write
	s.Model = sess.Model.ID
	s.Provider = sess.Model.ProviderID
	s.Mode = sess.Agent
	if sess.Title != "" {
		s.Title = sess.Title
	}

	if sess.Time.Created > 0 {
		s.Started = time.UnixMilli(sess.Time.Created)
	}
	if sess.Time.Updated > 0 {
		s.LastAct = time.UnixMilli(sess.Time.Updated)
	}
	if !s.Started.IsZero() && !s.LastAct.IsZero() {
		s.Duration = s.LastAct.Sub(s.Started)
	}

	readOpenCodeMessages(sessionID, s)
}

// readOpenCodeMessages fetches the message list from the OpenCode server API.
func readOpenCodeMessages(sessionID string, s *tokenStats) {
	client := &http.Client{Timeout: 500 * time.Millisecond}
	resp, err := client.Get(opencodeServer + "/session/" + sessionID + "/message")
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}

	var msgs ocMessageListResponse
	if json.Unmarshal(body, &msgs) != nil {
		return
	}

	if s.Tools == nil {
		s.Tools = make(map[string]int)
	}
	for _, m := range msgs {
		if m.Info.Role == "assistant" {
			s.Messages++
		}
		for _, p := range m.Parts {
			if p.Type == "tool" && p.Tool != "" {
				s.Tools[p.Tool]++
				s.ToolTotal++
			}
		}
	}
}

// readOpenCodeDisk reads completed OpenCode session data from disk storage.
func readOpenCodeDisk(sessionID string, s *tokenStats) {
	msgDir := openCodeMessageDir(sessionID)
	if msgDir == "" {
		return
	}
	entries, err := os.ReadDir(msgDir)
	if err != nil {
		return
	}

	var firstTS, lastTS time.Time

	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(msgDir, entry.Name()))
		if err != nil {
			continue
		}
		var msg struct {
			Role       string  `json:"role"`
			ModelID    string  `json:"modelID"`
			ProviderID string  `json:"providerID"`
			Mode       string  `json:"mode"`
			Cost       float64 `json:"cost"`
			Time       struct {
				Created int64 `json:"created"`
			} `json:"time"`
			Tokens struct {
				Input     int `json:"input"`
				Output    int `json:"output"`
				Reasoning int `json:"reasoning"`
				Cache     struct {
					Read  int `json:"read"`
					Write int `json:"write"`
				} `json:"cache"`
			} `json:"tokens"`
		}
		if json.Unmarshal(data, &msg) != nil {
			continue
		}

		s.Cost += msg.Cost
		s.InputT += msg.Tokens.Input
		s.OutputT += msg.Tokens.Output
		s.ReasonT += msg.Tokens.Reasoning
		s.CacheR += msg.Tokens.Cache.Read
		s.CacheW += msg.Tokens.Cache.Write

		if msg.ModelID != "" {
			s.Model = msg.ModelID
		}
		if msg.ProviderID != "" {
			s.Provider = msg.ProviderID
		}
		if msg.Mode != "" {
			s.Mode = msg.Mode
		}
		if msg.Role == "assistant" {
			s.Messages++
		}
		if msg.Time.Created > 0 {
			ts := time.UnixMilli(msg.Time.Created)
			if firstTS.IsZero() || ts.Before(firstTS) {
				firstTS = ts
			}
			if ts.After(lastTS) {
				lastTS = ts
			}
		}
	}

	s.Started = firstTS
	s.LastAct = lastTS
	if !firstTS.IsZero() && !lastTS.IsZero() {
		s.Duration = lastTS.Sub(firstTS)
	}
}

func openCodeMessageDir(sessionID string) string {
	h := homeDir()
	if h == "" {
		return ""
	}
	return filepath.Join(h, ".local", "share", "opencode", "storage", "message", sessionID)
}
