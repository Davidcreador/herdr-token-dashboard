package main

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testSessionID = "5f0a3c1e-2b4d-4f6a-8c9e-0d1e2f3a4b5c"

// withClaudeProjectsRoot points the Claude transcript lookup at a temp
// fixture root for the duration of one test.
func withClaudeProjectsRoot(t *testing.T, root string) {
	t.Helper()
	orig := claudeProjectsRoot
	claudeProjectsRoot = func() string { return root }
	t.Cleanup(func() { claudeProjectsRoot = orig })
}

// writeTranscript writes a JSONL fixture under root/dir/<sessionID>.jsonl.
func writeTranscript(t *testing.T, root, dir string, lines []string) {
	t.Helper()
	projDir := filepath.Join(root, dir)
	if err := os.MkdirAll(projDir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(projDir, testSessionID+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestReadClaudeSession(t *testing.T) {
	cases := []struct {
		name         string
		dir          string // project dir under the fixture root; "" = munged cwd
		cwd          string
		lines        []string
		wantMsgs     int
		wantIn       int
		wantOut      int
		wantCacheR   int
		wantCacheW   int
		wantCost     float64
		wantModel    string
		wantProvider string
		wantTools    map[string]int
		wantDuration time.Duration
	}{
		{
			name: "normal session",
			cwd:  "/home/user/projects/demo",
			lines: []string{
				`{"type":"user","timestamp":"2026-07-13T10:00:00.000Z","message":{"role":"user","content":"hello"}}`,
				`{"type":"assistant","timestamp":"2026-07-13T10:00:05.000Z","requestId":"req_01","message":{"id":"msg_01","model":"claude-opus-4-8","usage":{"input_tokens":1000,"output_tokens":500,"cache_read_input_tokens":2000,"cache_creation_input_tokens":3000,"service_tier":"standard"},"content":[{"type":"text","text":"hi"}]}}`,
				`{"type":"assistant","timestamp":"2026-07-13T10:05:00.000Z","requestId":"req_02","message":{"id":"msg_02","model":"claude-opus-4-8","usage":{"input_tokens":100,"output_tokens":50,"cache_read_input_tokens":0,"cache_creation_input_tokens":0},"content":[{"type":"tool_use","id":"toolu_01","name":"Bash"},{"type":"tool_use","id":"toolu_02","name":"Read"}]}}`,
			},
			wantMsgs:   2,
			wantIn:     1100,
			wantOut:    550,
			wantCacheR: 2000,
			wantCacheW: 3000,
			// opus-4-8 at $5/$25 (cache read 0.1x in, cache write 1.25x in):
			// (1000*5 + 500*25 + 2000*0.5 + 3000*6.25)/1e6
			//       + (100*5 + 50*25)/1e6
			wantCost:     0.03725 + 0.00175,
			wantModel:    "claude-opus-4-8",
			wantProvider: "anthropic",
			wantTools:    map[string]int{"Bash": 1, "Read": 1},
			wantDuration: 5 * time.Minute,
		},
		{
			name: "dedupe repeated message id and requestId",
			cwd:  "/home/user/projects/demo",
			lines: []string{
				`{"type":"assistant","timestamp":"2026-07-13T11:00:00.000Z","requestId":"req_01","message":{"id":"msg_01","model":"claude-sonnet-4-5","usage":{"input_tokens":100,"output_tokens":10,"cache_read_input_tokens":0,"cache_creation_input_tokens":0},"content":[{"type":"tool_use","id":"toolu_x","name":"Bash"}]}}`,
				`{"type":"assistant","timestamp":"2026-07-13T11:00:03.000Z","requestId":"req_01","message":{"id":"msg_01","model":"claude-sonnet-4-5","usage":{"input_tokens":100,"output_tokens":200,"cache_read_input_tokens":0,"cache_creation_input_tokens":0},"content":[{"type":"tool_use","id":"toolu_x","name":"Bash"}]}}`,
			},
			wantMsgs: 1,
			wantIn:   100,
			wantOut:  200,
			// sonnet-4-5, last occurrence wins: (100*3 + 200*15)/1e6
			wantCost:     0.0033,
			wantModel:    "claude-sonnet-4-5",
			wantProvider: "anthropic",
			wantTools:    map[string]int{"Bash": 1},
			wantDuration: 3 * time.Second,
		},
		{
			name: "unknown model has zero cost but tokens are kept",
			cwd:  "/home/user/projects/demo",
			lines: []string{
				`{"type":"assistant","timestamp":"2026-07-13T12:00:00.000Z","requestId":"req_01","message":{"id":"msg_01","model":"claude-zeta-9","usage":{"input_tokens":1000,"output_tokens":1000,"cache_read_input_tokens":0,"cache_creation_input_tokens":0},"content":[{"type":"text","text":"hi"}]}}`,
			},
			wantMsgs:     1,
			wantIn:       1000,
			wantOut:      1000,
			wantCost:     0,
			wantModel:    "claude-zeta-9",
			wantProvider: "anthropic",
			wantTools:    map[string]int{},
		},
		{
			name: "glob fallback when munged cwd path misses",
			dir:  "-home-user-projects-old-location",
			cwd:  "/home/user/projects/moved-since-launch",
			lines: []string{
				`{"type":"assistant","timestamp":"2026-07-13T13:00:00.000Z","requestId":"req_01","message":{"id":"msg_01","model":"claude-haiku-4-5","usage":{"input_tokens":500,"output_tokens":100,"cache_read_input_tokens":0,"cache_creation_input_tokens":0},"content":[{"type":"text","text":"hi"}]}}`,
			},
			wantMsgs: 1,
			wantIn:   500,
			wantOut:  100,
			// haiku-4-5: (500*1 + 100*5)/1e6
			wantCost:     0.001,
			wantModel:    "claude-haiku-4-5",
			wantProvider: "anthropic",
			wantTools:    map[string]int{},
		},
		{
			name: "malformed lines are skipped",
			cwd:  "/home/user/projects/demo",
			lines: []string{
				`not json at all`,
				`{"type":"assistant","message":`,
				`{"type":"assistant","timestamp":"2026-07-13T14:00:00.000Z","requestId":"req_01","message":{"id":"msg_01","model":"claude-fable-5","usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":0,"cache_creation_input_tokens":0},"content":"plain string content"}}`,
				`{"type":"assistant"}`,
			},
			wantMsgs: 1,
			wantIn:   10,
			wantOut:  20,
			// fable-5: (10*10 + 20*50)/1e6
			wantCost:     0.0011,
			wantModel:    "claude-fable-5",
			wantProvider: "anthropic",
			wantTools:    map[string]int{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			withClaudeProjectsRoot(t, root)

			dir := tc.dir
			if dir == "" {
				dir = mungeClaudePath(tc.cwd)
			}
			writeTranscript(t, root, dir, tc.lines)

			s := tokenStats{Tools: map[string]int{}}
			readClaudeSession(testSessionID, tc.cwd, &s)

			if s.Messages != tc.wantMsgs {
				t.Errorf("Messages = %d, want %d", s.Messages, tc.wantMsgs)
			}
			if s.InputT != tc.wantIn {
				t.Errorf("InputT = %d, want %d", s.InputT, tc.wantIn)
			}
			if s.OutputT != tc.wantOut {
				t.Errorf("OutputT = %d, want %d", s.OutputT, tc.wantOut)
			}
			if s.CacheR != tc.wantCacheR {
				t.Errorf("CacheR = %d, want %d", s.CacheR, tc.wantCacheR)
			}
			if s.CacheW != tc.wantCacheW {
				t.Errorf("CacheW = %d, want %d", s.CacheW, tc.wantCacheW)
			}
			if math.Abs(s.Cost-tc.wantCost) > 1e-9 {
				t.Errorf("Cost = %v, want %v", s.Cost, tc.wantCost)
			}
			if s.Model != tc.wantModel {
				t.Errorf("Model = %q, want %q", s.Model, tc.wantModel)
			}
			if s.Provider != tc.wantProvider {
				t.Errorf("Provider = %q, want %q", s.Provider, tc.wantProvider)
			}
			if s.Duration != tc.wantDuration {
				t.Errorf("Duration = %v, want %v", s.Duration, tc.wantDuration)
			}
			if len(s.Tools) != len(tc.wantTools) {
				t.Errorf("Tools = %v, want %v", s.Tools, tc.wantTools)
			}
			toolTotal := 0
			for name, n := range tc.wantTools {
				toolTotal += n
				if s.Tools[name] != n {
					t.Errorf("Tools[%q] = %d, want %d", name, s.Tools[name], n)
				}
			}
			if s.ToolTotal != toolTotal {
				t.Errorf("ToolTotal = %d, want %d", s.ToolTotal, toolTotal)
			}
		})
	}
}

func TestReadClaudeSessionMissingTranscript(t *testing.T) {
	withClaudeProjectsRoot(t, t.TempDir())

	s := tokenStats{Tools: map[string]int{}}
	readClaudeSession(testSessionID, "/home/user/projects/nowhere", &s)

	if s.Messages != 0 || s.InputT != 0 || s.Cost != 0 {
		t.Errorf("expected zero stats for missing transcript, got %+v", s)
	}
}

func TestClaudeRates(t *testing.T) {
	cases := []struct {
		model  string
		wantIn float64
		wantOK bool
	}{
		{"claude-opus-4-8", 5, true},
		{"claude-opus-5-5", 4, true},
		{"claude-opus-5", 5, true},
		{"claude-sonnet-5-5", 2, true},
		{"claude-sonnet-5", 2, true},
		{"claude-opus-4-1", 15, true},
		{"claude-sonnet-4-5", 3, true},
		{"claude-sonnet-4-20250514", 3, true},
		{"claude-haiku-4-5", 1, true},
		{"claude-fable-5", 10, true},
		{"claude-fable-5-1", 10, true},
		{"claude-mythos-5-1", 10, true},
		{"some-future-model", 0, false},
		{"", 0, false},
	}
	for _, tc := range cases {
		in, _, ok := claudeRates(tc.model)
		if ok != tc.wantOK || in != tc.wantIn {
			t.Errorf("claudeRates(%q) = (in=%v, ok=%v), want (in=%v, ok=%v)",
				tc.model, in, ok, tc.wantIn, tc.wantOK)
		}
	}
}

func TestClaudeCacheReadMultiplier(t *testing.T) {
	cases := []struct {
		model string
		want  float64
	}{
		{"claude-fable-5-1", 0.025},
		{"claude-mythos-5-1", 0.025},
		{"claude-opus-5-5", 0.05},
		{"claude-sonnet-5-5", 0.1},
	}
	for _, tc := range cases {
		got := claudeCacheReadMultiplier(tc.model)
		if got != tc.want {
			t.Errorf("claudeCacheReadMultiplier(%q) = %v, want %v", tc.model, got, tc.want)
		}
	}
}

func TestMungeClaudePath(t *testing.T) {
	got := mungeClaudePath("/home/ajavaherian/projects/TMM-Workflow")
	want := "-home-ajavaherian-projects-TMM-Workflow"
	if got != want {
		t.Errorf("mungeClaudePath = %q, want %q", got, want)
	}
	if got := mungeClaudePath("/tmp/a_b.c d"); got != "-tmp-a-b-c-d" {
		t.Errorf("mungeClaudePath = %q, want %q", got, "-tmp-a-b-c-d")
	}
}
