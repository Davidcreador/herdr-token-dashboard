package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// codexSessionsRoot returns the directory Codex writes rollouts under.
func codexSessionsRoot() string {
	if custom := os.Getenv("CODEX_HOME"); custom != "" {
		return filepath.Join(custom, "sessions")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex", "sessions")
}

// codexRolloutPath locates the rollout for a session id. Codex lays rollouts
// out as sessions/YYYY/MM/DD/rollout-<timestamp>-<session-id>.jsonl, so the id
// is matched on the filename suffix; if that misses (a layout change, or a
// session id that is not in the name) the session_meta line is checked instead.
func codexRolloutPath(sessionID string) string {
	root := codexSessionsRoot()
	if root == "" || sessionID == "" {
		return ""
	}

	matches, err := filepath.Glob(filepath.Join(root, "*", "*", "*", "rollout-*"+sessionID+".jsonl"))
	if err == nil && len(matches) > 0 {
		return matches[0]
	}

	candidates, err := filepath.Glob(filepath.Join(root, "*", "*", "*", "rollout-*.jsonl"))
	if err != nil {
		return ""
	}
	for _, candidate := range candidates {
		if codexRolloutSessionID(candidate) == sessionID {
			return candidate
		}
	}
	return ""
}

// codexRolloutSessionID reads the session id from a rollout's session_meta,
// which is the first line.
func codexRolloutSessionID(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.SplitN(string(data), "\n", 8) {
		if line == "" {
			continue
		}
		var entry struct {
			Type    string `json:"type"`
			Payload struct {
				SessionID string `json:"session_id"`
				ID        string `json:"id"`
			} `json:"payload"`
		}
		if json.Unmarshal([]byte(line), &entry) != nil || entry.Type != "session_meta" {
			continue
		}
		if entry.Payload.SessionID != "" {
			return entry.Payload.SessionID
		}
		return entry.Payload.ID
	}
	return ""
}

// readCodexSession reads a Codex rollout JSONL and extracts tokens, model,
// message count, tool calls, and session duration.
//
// Codex reports cumulative totals on every token_count event, so the last one
// wins rather than being summed — unlike Pi and Claude, where per-message usage
// is accumulated.
//
// No cost is set: the pricing table in main.go covers Anthropic models only,
// and inventing OpenAI rates here would produce a confidently wrong number.
// Unknown-model behaviour already renders tokens with a blank cost.
func readCodexSession(sessionID string, s *tokenStats) {
	path := codexRolloutPath(sessionID)
	if path == "" {
		debugLog("codex rollout not found for session " + sessionID)
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		debugLog("codex rollout unreadable: " + err.Error())
		return
	}

	var firstTS, lastTS time.Time

	for _, raw := range strings.Split(string(data), "\n") {
		if raw == "" {
			continue
		}
		var entry struct {
			Type      string          `json:"type"`
			Timestamp string          `json:"timestamp"`
			Payload   json.RawMessage `json:"payload"`
		}
		if json.Unmarshal([]byte(raw), &entry) != nil {
			continue
		}

		if ts, err := time.Parse(time.RFC3339, entry.Timestamp); err == nil {
			if firstTS.IsZero() || ts.Before(firstTS) {
				firstTS = ts
			}
			if ts.After(lastTS) {
				lastTS = ts
			}
		}

		switch entry.Type {
		case "session_meta":
			var meta struct {
				Cwd string `json:"cwd"`
			}
			if json.Unmarshal(entry.Payload, &meta) == nil && meta.Cwd != "" && s.Cwd == "" {
				s.Cwd = meta.Cwd
			}
		case "turn_context", "world_state":
			// turn_context carries the model for the active turn; world_state is
			// the fallback for rollouts that predate it.
			var ctx struct {
				Model string `json:"model"`
			}
			if json.Unmarshal(entry.Payload, &ctx) == nil && ctx.Model != "" {
				s.Model = ctx.Model
			}
		case "event_msg":
			var msg struct {
				Type string `json:"type"`
				Info struct {
					TotalTokenUsage struct {
						InputTokens           int `json:"input_tokens"`
						CachedInputTokens     int `json:"cached_input_tokens"`
						CacheWriteInputTokens int `json:"cache_write_input_tokens"`
						OutputTokens          int `json:"output_tokens"`
						ReasoningOutputTokens int `json:"reasoning_output_tokens"`
					} `json:"total_token_usage"`
					ModelContextWindow int `json:"model_context_window"`
				} `json:"info"`
			}
			if json.Unmarshal(entry.Payload, &msg) != nil {
				continue
			}
			switch msg.Type {
			case "token_count":
				usage := msg.Info.TotalTokenUsage
				// Codex's input_tokens is INCLUSIVE of cached_input_tokens,
				// whereas this dashboard's other sources report uncached input.
				// Subtract so the IN and CACHE columns mean the same thing for
				// every agent and the totals row stays additive.
				uncached := usage.InputTokens - usage.CachedInputTokens - usage.CacheWriteInputTokens
				if uncached < 0 {
					uncached = 0
				}
				s.InputT = uncached
				s.OutputT = usage.OutputTokens
				s.ReasonT = usage.ReasoningOutputTokens
				s.CacheR = usage.CachedInputTokens
				s.CacheW = usage.CacheWriteInputTokens
			case "agent_message":
				s.Messages++
			}
		case "response_item":
			var item struct {
				Type string `json:"type"`
				Name string `json:"name"`
			}
			if json.Unmarshal(entry.Payload, &item) != nil {
				continue
			}
			if item.Type == "custom_tool_call" || item.Type == "function_call" {
				name := item.Name
				if name == "" {
					name = "tool"
				}
				s.Tools[name]++
				s.ToolTotal++
			}
		}
	}

	s.Provider = "openai"
	s.Cost = codexCost(s.Model, s.InputT, s.OutputT, s.CacheR, s.CacheW)
	s.Started = firstTS
	s.LastAct = lastTS
	if !firstTS.IsZero() && lastTS.After(firstTS) {
		s.Duration = lastTS.Sub(firstTS)
	}
}

// openaiPricing maps a model-id substring to OpenAI per-MTok USD rates
// (standard tier, short context). Matching is by substring, longest match
// first, so "gpt-5.6-sol" wins over "gpt-5".
//
// Scoped to the gpt-5 family, which is what Codex runs. Those models price
// cached input at 0.1x input and cache writes at 1.25x, matching blendedCost.
// Older families do NOT (gpt-4.1 caches at 0.25x, gpt-4o at 0.5x) and are
// deliberately omitted rather than costed with the wrong multiplier.
//
// Long-context rates (roughly double) are not modelled: they apply above a
// context length larger than the window Codex reports (258,400 for
// gpt-5.6-sol), so a Codex turn cannot reach that tier.
var openaiPricing = []struct {
	substr string
	in     float64 // USD per MTok input
	out    float64 // USD per MTok output
}{
	{"gpt-5.6-sol", 5, 30},
	{"gpt-5.6-terra", 2, 12},
	{"gpt-5.6-luna", 0.2, 1.2},
	{"gpt-5.6-cyber", 12.5, 75},
	{"gpt-5.5-cyber", 12.5, 75},
	{"gpt-5.5-pro", 30, 180},
	{"gpt-5.5", 5, 30},
	{"gpt-5.4-pro", 30, 180},
	{"gpt-5.4-mini", 0.75, 4.5},
	{"gpt-5.4-nano", 0.2, 1.25},
	{"gpt-5.4", 2.5, 15},
	{"gpt-5.3-codex", 1.75, 14},
	{"gpt-5.2-pro", 21, 168},
	{"gpt-5.2", 1.75, 14},
	{"gpt-5.1", 1.25, 10},
	{"gpt-5-pro", 15, 120},
	{"gpt-5-mini", 0.25, 2},
	{"gpt-5-nano", 0.05, 0.4},
	{"gpt-5", 1.25, 10},
}

// openaiRates returns per-MTok rates for a model id, longest substring first.
// ok is false for unknown models, which then show tokens with no cost.
func openaiRates(model string) (in, out float64, ok bool) {
	best := -1
	for _, p := range openaiPricing {
		if strings.Contains(model, p.substr) && len(p.substr) > best {
			best = len(p.substr)
			in, out = p.in, p.out
			ok = true
		}
	}
	return in, out, ok
}

// codexCost estimates the USD cost of a Codex session.
func codexCost(model string, input, output, cacheRead, cacheWrite int) float64 {
	in, out, ok := openaiRates(model)
	if !ok {
		return 0
	}
	return blendedCost(in, out, input, output, cacheRead, cacheWrite)
}
