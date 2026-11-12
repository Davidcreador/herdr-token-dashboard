package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// copilotRoot returns the root directory for Copilot CLI state (~/.copilot).
func copilotRoot() string {
	if custom := os.Getenv("COPILOT_HOME"); custom != "" {
		return custom
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".copilot")
}

// copilotSessionDir returns the path to a session's directory under ~/.copilot/session-state/<id>.
func copilotSessionDir(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	// If already a directory or file path
	if fi, err := os.Stat(sessionID); err == nil {
		if fi.IsDir() {
			return sessionID
		}
		return filepath.Dir(sessionID)
	}
	root := copilotRoot()
	if root == "" {
		return ""
	}
	p := filepath.Join(root, "session-state", sessionID)
	if fi, err := os.Stat(p); err == nil && fi.IsDir() {
		return p
	}
	return ""
}

// copilotEventsPath finds the events.jsonl for a session id or path.
func copilotEventsPath(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	if strings.HasSuffix(sessionID, "events.jsonl") {
		if _, err := os.Stat(sessionID); err == nil {
			return sessionID
		}
	}
	dir := copilotSessionDir(sessionID)
	if dir == "" {
		return ""
	}
	p := filepath.Join(dir, "events.jsonl")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

type copilotWorkspace struct {
	ID        string
	Cwd       string
	Name      string
	UpdatedAt time.Time
}

// parseCopilotWorkspace reads a workspace.yaml and extracts basic metadata.
func parseCopilotWorkspace(path string) (copilotWorkspace, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return copilotWorkspace{}, false
	}
	var ws copilotWorkspace
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "id:") {
			ws.ID = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
		} else if strings.HasPrefix(line, "cwd:") {
			ws.Cwd = strings.TrimSpace(strings.TrimPrefix(line, "cwd:"))
		} else if strings.HasPrefix(line, "name:") {
			ws.Name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
		} else if strings.HasPrefix(line, "updated_at:") {
			raw := strings.TrimSpace(strings.TrimPrefix(line, "updated_at:"))
			if t, err := time.Parse(time.RFC3339, raw); err == nil {
				ws.UpdatedAt = t
			} else if t, err := time.Parse("2006-01-02T15:04:05.999Z", raw); err == nil {
				ws.UpdatedAt = t
			}
		}
	}
	return ws, ws.ID != ""
}

// findCopilotSession resolves the most appropriate Copilot session ID for a Herdr pane.
func findCopilotSession(p paneEntry) string {
	// 1. Direct session ID from Herdr's agent_session
	if p.AgentSession != nil && p.AgentSession.Value != "" {
		return p.AgentSession.Value
	}

	root := copilotRoot()
	if root == "" {
		return ""
	}
	stateDir := filepath.Join(root, "session-state")
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		return ""
	}

	// Clean candidate title from terminal title (e.g. "Uninstall Quoder - GitHub Copilot" -> "Uninstall Quoder")
	targetTitle := strings.TrimSpace(p.TerminalTitle)
	if targetTitle == "" {
		targetTitle = strings.TrimSpace(p.Title)
	}
	targetTitle = strings.TrimSuffix(targetTitle, " - GitHub Copilot")
	targetTitle = strings.TrimSpace(targetTitle)

	var (
		titleMatchID string
		cwdMatches   []copilotWorkspace
	)

	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		wsPath := filepath.Join(stateDir, entry.Name(), "workspace.yaml")
		ws, ok := parseCopilotWorkspace(wsPath)
		if !ok {
			ws.ID = entry.Name()
		}

		// 2. Terminal title match
		if targetTitle != "" && ws.Name != "" && strings.EqualFold(ws.Name, targetTitle) {
			if p.Cwd == "" || ws.Cwd == "" || ws.Cwd == p.Cwd {
				titleMatchID = ws.ID
				break
			}
		}

		// Collect matches for cwd
		if p.Cwd != "" && ws.Cwd == p.Cwd {
			// If UpdatedAt was not in YAML, fallback to directory modification time
			if ws.UpdatedAt.IsZero() {
				if fi, err := entry.Info(); err == nil {
					ws.UpdatedAt = fi.ModTime()
				}
			}
			cwdMatches = append(cwdMatches, ws)
		}
	}

	if titleMatchID != "" {
		return titleMatchID
	}

	// 3. Match latest modified session for pane's CWD
	if len(cwdMatches) > 0 {
		sort.Slice(cwdMatches, func(i, j int) bool {
			return cwdMatches[i].UpdatedAt.After(cwdMatches[j].UpdatedAt)
		})
		return cwdMatches[0].ID
	}

	return ""
}

// JSON structures for Copilot events.jsonl
type copilotEvent struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Data      json.RawMessage `json:"data"`
}

type copilotUsageRecordData struct {
	Usage struct {
		Model            string         `json:"model"`
		ModelDisplayName string         `json:"modelDisplayName"`
		InputTokens      int            `json:"inputTokens"`
		OutputTokens     int            `json:"outputTokens"`
		CacheReadTokens  int            `json:"cacheReadTokens"`
		CacheWriteTokens int            `json:"cacheWriteTokens"`
		ReasoningTokens  int            `json:"reasoningTokens"`
		ToolCounts       map[string]int `json:"toolCounts"`
		CopilotUsage     struct {
			TotalNanoAiu int64 `json:"totalNanoAiu"`
		} `json:"copilotUsage"`
	} `json:"usage"`
}

type copilotQuotaData struct {
	QuotaSnapshots struct {
		PremiumInteractions struct {
			EntitlementRequests int     `json:"entitlementRequests"`
			UsedRequests        int     `json:"usedRequests"`
			RemainingPercentage float64 `json:"remainingPercentage"`
			ResetDate           string  `json:"resetDate"`
		} `json:"premium_interactions"`
	} `json:"quotaSnapshots"`
}

// readCopilotSession reads events.jsonl for a Copilot session and populates tokenStats.
func readCopilotSession(sessionID string, s *tokenStats) {
	if s.Tools == nil {
		s.Tools = map[string]int{}
	}

	path := copilotEventsPath(sessionID)
	if path == "" {
		return
	}

	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()

	var (
		firstTS      time.Time
		lastTS       time.Time
		totalNanoAiu int64
		seenIDs      = map[string]bool{}
	)

	scanner := bufio.NewScanner(file)
	// Buffer up to 1MB lines (some Copilot events contain context frames)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		var ev copilotEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}

		if ev.ID != "" {
			if seenIDs[ev.ID] {
				continue
			}
			seenIDs[ev.ID] = true
		}

		if ev.Timestamp != "" {
			if t, err := time.Parse(time.RFC3339Nano, ev.Timestamp); err == nil {
				if firstTS.IsZero() || t.Before(firstTS) {
					firstTS = t
				}
				if t.After(lastTS) {
					lastTS = t
				}
			} else if t, err := time.Parse(time.RFC3339, ev.Timestamp); err == nil {
				if firstTS.IsZero() || t.Before(firstTS) {
					firstTS = t
				}
				if t.After(lastTS) {
					lastTS = t
				}
			}
		}

		switch ev.Type {
		case "session.usage_record":
			var d copilotUsageRecordData
			if err := json.Unmarshal(ev.Data, &d); err == nil {
				u := d.Usage
				s.InputT += u.InputTokens
				s.OutputT += u.OutputTokens
				s.CacheR += u.CacheReadTokens
				s.CacheW += u.CacheWriteTokens
				s.ReasonT += u.ReasoningTokens
				totalNanoAiu += u.CopilotUsage.TotalNanoAiu

				if u.Model != "" {
					s.Model = u.Model
				}

				for tool, count := range u.ToolCounts {
					s.Tools[tool] += count
					s.ToolTotal += count
				}
			}

		case "user.message", "assistant.message":
			s.Messages++
		}

		// Extract quota snapshot if present in this event payload
		if bytes.Contains(ev.Data, []byte("quotaSnapshots")) && bytes.Contains(ev.Data, []byte("premium_interactions")) {
			var q copilotQuotaData
			if err := json.Unmarshal(ev.Data, &q); err == nil {
				prem := q.QuotaSnapshots.PremiumInteractions
				if prem.EntitlementRequests > 0 {
					s.QuotaLimit = prem.EntitlementRequests
					s.QuotaUsed = prem.UsedRequests
					s.QuotaRemPct = prem.RemainingPercentage
				}
			}
		}
	}

	if totalNanoAiu > 0 {
		s.AICredits = float64(totalNanoAiu) / 1e9
	}

	if !firstTS.IsZero() && !lastTS.IsZero() {
		s.Duration = lastTS.Sub(firstTS)
		s.LastAct = lastTS
	}

	s.Provider = "github"
	s.Source = "copilot"
	s.Cost = 0.0
}
