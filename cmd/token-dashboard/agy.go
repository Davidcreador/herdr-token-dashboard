package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var reModelSelection = regexp.MustCompile("`Model Selection` from \\S+ to (.+?)(?:\\.\\s|\\.\n|\\.$|\\n|$)")

// agyBrainRoot returns the root directory where Antigravity CLI writes session brains.
func agyBrainRoot() string {
	if custom := os.Getenv("ANTIGRAVITY_APP_DATA_DIR"); custom != "" {
		return filepath.Join(custom, "brain")
	}
	if custom := os.Getenv("GEMINI_HOME"); custom != "" {
		return filepath.Join(custom, "antigravity-cli", "brain")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".gemini", "antigravity-cli", "brain")
}

// agyTranscriptPath locates the transcript.jsonl for a session id.
func agyTranscriptPath(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	// If sessionID is already a path to a transcript file
	if strings.HasSuffix(sessionID, "transcript.jsonl") || strings.HasSuffix(sessionID, "transcript_full.jsonl") {
		if _, err := os.Stat(sessionID); err == nil {
			return sessionID
		}
	}
	root := agyBrainRoot()
	if root == "" {
		return ""
	}

	// 1. Exact path: brain/<sessionID>/.system_generated/logs/transcript.jsonl
	p := filepath.Join(root, sessionID, ".system_generated", "logs", "transcript.jsonl")
	if _, err := os.Stat(p); err == nil {
		return p
	}

	// 2. Exact path: transcript_full.jsonl
	pFull := filepath.Join(root, sessionID, ".system_generated", "logs", "transcript_full.jsonl")
	if _, err := os.Stat(pFull); err == nil {
		return pFull
	}

	// 3. Fallback: glob search if sessionID is prefixed
	matches, err := filepath.Glob(filepath.Join(root, "*"+sessionID+"*", ".system_generated", "logs", "transcript.jsonl"))
	if err == nil && len(matches) > 0 {
		return matches[0]
	}

	return ""
}

// agyDefaultModel reads the model configured in ~/.gemini/antigravity-cli/settings.json.
func agyDefaultModel() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "gemini-2.5-flash"
	}
	settingsPath := filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return "gemini-2.5-flash"
	}
	var settings struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(data, &settings); err == nil && settings.Model != "" {
		return settings.Model
	}
	return "gemini-2.5-flash"
}

// normalizeGeminiModel cleans and maps verbose Antigravity model names to standard identifiers.
func normalizeGeminiModel(raw string) string {
	raw = strings.TrimSpace(raw)
	lower := strings.ToLower(raw)
	switch {
	case strings.Contains(lower, "3.8") && strings.Contains(lower, "flash"):
		return "gemini-3.8-flash"
	case strings.Contains(lower, "3") && strings.Contains(lower, "flash"):
		return "gemini-3-flash"
	case strings.Contains(lower, "2.5") && strings.Contains(lower, "pro"):
		return "gemini-2.5-pro"
	case strings.Contains(lower, "2.5") && strings.Contains(lower, "flash"):
		return "gemini-2.5-flash"
	case strings.Contains(lower, "2.0") && strings.Contains(lower, "flash"):
		return "gemini-2.0-flash"
	case strings.Contains(lower, "1.5") && strings.Contains(lower, "pro"):
		return "gemini-1.5-pro"
	case strings.Contains(lower, "1.5") && strings.Contains(lower, "flash"):
		return "gemini-1.5-flash"
	case strings.Contains(lower, "flash-lite") || strings.Contains(lower, "flash_lite"):
		return "gemini-flash-lite"
	case strings.Contains(lower, "pro"):
		return "gemini-pro"
	case strings.Contains(lower, "flash"):
		return "gemini-flash"
	default:
		if raw != "" {
			return strings.ReplaceAll(strings.ToLower(raw), " ", "-")
		}
		return "gemini-flash"
	}
}

// extractModelFromPrompt parses user prompts for in-session model selection changes.
func extractModelFromPrompt(content string) string {
	matches := reModelSelection.FindStringSubmatch(content)
	if len(matches) > 1 {
		return strings.TrimSpace(matches[1])
	}
	return ""
}

// readAgySession reads an Antigravity CLI transcript.jsonl and extracts token counts,
// model, message count, tool calls, and session duration.
func readAgySession(sessionID string, s *tokenStats) {
	path := agyTranscriptPath(sessionID)
	if path == "" {
		debugLog("agy transcript not found for session " + sessionID)
		return
	}

	data, err := os.ReadFile(path)
	if err != nil {
		debugLog("agy transcript unreadable: " + err.Error())
		return
	}

	var firstTS, lastTS time.Time
	model := agyDefaultModel()

	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(line) == 0 {
			continue
		}

		var entry struct {
			Type            string `json:"type"`
			Source          string `json:"source"`
			CreatedAt       string `json:"created_at"`
			InputTokens     *int   `json:"input_tokens"`
			OutputTokens    *int   `json:"output_tokens"`
			CacheReadTokens *int   `json:"cache_read_tokens"`
			Content         string `json:"content"`
			ToolCalls       []struct {
				Name string `json:"name"`
			} `json:"tool_calls"`
		}

		if json.Unmarshal(line, &entry) != nil {
			continue
		}

		if ts, err := time.Parse(time.RFC3339, entry.CreatedAt); err == nil {
			if firstTS.IsZero() || ts.Before(firstTS) {
				firstTS = ts
			}
			if ts.After(lastTS) {
				lastTS = ts
			}
		}

		// Detect in-session model switches if any
		if entry.Type == "USER_INPUT" && strings.Contains(entry.Content, "Model Selection") {
			if m := extractModelFromPrompt(entry.Content); m != "" {
				model = m
			}
		}

		if entry.Type == "PLANNER_RESPONSE" {
			s.Messages++
			if entry.InputTokens != nil {
				s.InputT += *entry.InputTokens
			}
			if entry.OutputTokens != nil {
				s.OutputT += *entry.OutputTokens
			}
			if entry.CacheReadTokens != nil {
				s.CacheR += *entry.CacheReadTokens
			}
			for _, tc := range entry.ToolCalls {
				name := tc.Name
				if name == "" {
					name = "tool"
				}
				s.Tools[name]++
				s.ToolTotal++
			}
		}
	}

	s.Provider = "google"
	s.Model = normalizeGeminiModel(model)
	s.Cost = agyCost(s.Model, s.InputT, s.OutputT, s.CacheR, s.CacheW)
	s.Started = firstTS
	s.LastAct = lastTS
	if !firstTS.IsZero() && lastTS.After(firstTS) {
		s.Duration = lastTS.Sub(firstTS)
	}
}

// geminiPricing maps model substrings to Google Gemini per-MTok USD rates.
var geminiPricing = []struct {
	substr string
	in     float64 // USD per MTok input
	out    float64 // USD per MTok output
}{
	// Gemini Pro models
	{"gemini-2.5-pro", 1.25, 10.00},
	{"gemini-1.5-pro", 1.25, 5.00},
	{"gemini-pro", 1.25, 10.00},
	{"pro", 1.25, 10.00},

	// Gemini Flash models
	{"gemini-3.8-flash", 0.30, 2.50},
	{"gemini-3-flash", 0.30, 2.50},
	{"gemini-2.5-flash", 0.30, 2.50},
	{"gemini-2.0-flash", 0.15, 0.60},
	{"gemini-1.5-flash", 0.075, 0.30},
	{"flash-lite", 0.075, 0.30},
	{"flash", 0.30, 2.50},
	{"gemini", 0.30, 2.50},
}

// geminiRates returns per-MTok rates for a Gemini model, longest substring first.
func geminiRates(model string) (in, out float64, ok bool) {
	norm := strings.ToLower(model)
	norm = strings.ReplaceAll(norm, " ", "-")
	best := -1
	for _, p := range geminiPricing {
		if strings.Contains(norm, p.substr) && len(p.substr) > best {
			best = len(p.substr)
			in, out = p.in, p.out
			ok = true
		}
	}
	return in, out, ok
}

// agyCost estimates the USD cost of an AGY session.
func agyCost(model string, input, output, cacheRead, cacheWrite int) float64 {
	in, out, ok := geminiRates(model)
	if !ok {
		return 0
	}
	// Gemini cached input is billed at a 90% discount (0.10x of input token rate).
	cacheReadMultiplier := 0.10
	return blendedCostWithCacheRead(in, out, input, output, cacheRead, cacheWrite, cacheReadMultiplier)
}
