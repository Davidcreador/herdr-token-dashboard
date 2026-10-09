package main

// ── Claude Code spend history ───────────────────────────────────────────────
//
// The live table shows one row per OPEN pane, priced from that pane's current
// transcript — so `/clear` (a new session id) restarts it at $0, and a closed
// pane disappears. History instead scans EVERY transcript under
// ~/.claude/projects, subagents included, and buckets cost per project
// directory per local day. Files are read incrementally: each keeps a byte
// offset, so after the first pass a rescan only parses appended lines.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type histTurn struct {
	day  string // local date, 2006-01-02
	cost float64
}

type histFile struct {
	size, offset int64
	dir          string // top-level project directory under ~/.claude/projects
	sub          bool   // a subagent transcript
	turns        map[string]histTurn
}

type history struct {
	mu       sync.Mutex
	files    map[string]*histFile
	names    map[string]string // project dir -> display name, from a cwd seen in it
	scanned  time.Time
	scanning bool
}

var hist = &history{files: map[string]*histFile{}, names: map[string]string{}}

// histRescanEvery bounds how often the background scan runs.
const histRescanEvery = 30 * time.Second

// maybeScan starts a background rescan when none is running and the last one
// is older than histRescanEvery. It never blocks the UI.
func (h *history) maybeScan() {
	h.mu.Lock()
	if h.scanning || time.Since(h.scanned) < histRescanEvery {
		h.mu.Unlock()
		return
	}
	h.scanning = true
	h.mu.Unlock()
	go func() {
		h.scan()
		h.mu.Lock()
		h.scanning = false
		h.scanned = time.Now()
		h.mu.Unlock()
	}()
}

func (h *history) ready() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return !h.scanned.IsZero()
}

func (h *history) scan() {
	root := claudeProjectsRoot()
	if root == "" {
		return
	}
	main, _ := filepath.Glob(filepath.Join(root, "*", "*.jsonl"))
	subs, _ := filepath.Glob(filepath.Join(root, "*", "*", "subagents", "*.jsonl"))
	for _, list := range [][]string{main, subs} {
		for _, p := range list {
			h.scanFile(root, p)
		}
	}
}

func (h *history) scanFile(root, path string) {
	fi, err := os.Stat(path)
	if err != nil {
		return
	}
	h.mu.Lock()
	f := h.files[path]
	if f == nil {
		rel, _ := filepath.Rel(root, path)
		f = &histFile{
			dir:   strings.SplitN(rel, string(filepath.Separator), 2)[0],
			sub:   strings.Contains(rel, string(filepath.Separator)+"subagents"+string(filepath.Separator)),
			turns: map[string]histTurn{},
		}
		h.files[path] = f
	}
	if fi.Size() < f.offset { // truncated or replaced: start over
		f.offset, f.turns = 0, map[string]histTurn{}
	}
	start, needName := f.offset, h.names[f.dir] == ""
	h.mu.Unlock()
	if fi.Size() == start {
		return
	}

	fh, err := os.Open(path)
	if err != nil {
		return
	}
	defer fh.Close()
	if _, err := fh.Seek(start, io.SeekStart); err != nil {
		return
	}
	r := bufio.NewReaderSize(fh, 1<<20)
	consumed := start
	newTurns := map[string]histTurn{}
	name := ""
	for {
		line, err := r.ReadBytes('\n')
		if err != nil { // EOF or partial last line: leave it for the next scan
			break
		}
		consumed += int64(len(line))
		if needName && name == "" && !f.sub && bytes.Contains(line, []byte(`"cwd"`)) {
			var c struct {
				Cwd string `json:"cwd"`
			}
			// Only a cwd that IS this project directory names it: a session
			// that cd'd into a worktree would otherwise lend it the worktree's
			// name and two directories would share one label.
			if json.Unmarshal(line, &c) == nil && c.Cwd != "" && mungeClaudePath(c.Cwd) == f.dir {
				name = projectName(c.Cwd)
			}
		}
		if !bytes.Contains(line, []byte(`"usage"`)) || !bytes.Contains(line, []byte(`"assistant"`)) {
			continue
		}
		var e struct {
			Type      string `json:"type"`
			Timestamp string `json:"timestamp"`
			RequestID string `json:"requestId"`
			Message   struct {
				ID    string `json:"id"`
				Model string `json:"model"`
				Usage struct {
					InputTokens              int `json:"input_tokens"`
					OutputTokens             int `json:"output_tokens"`
					CacheReadInputTokens     int `json:"cache_read_input_tokens"`
					CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
					CacheCreation            struct {
						OneHour int `json:"ephemeral_1h_input_tokens"`
					} `json:"cache_creation"`
				} `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &e) != nil || e.Type != "assistant" || e.Message.ID == "" {
			continue
		}
		ts, err := time.Parse(time.RFC3339Nano, e.Timestamp)
		if err != nil {
			continue
		}
		u := e.Message.Usage
		newTurns[e.Message.ID+"\x00"+e.RequestID] = histTurn{
			day: ts.Local().Format("2006-01-02"),
			cost: claudeCostTTL(e.Message.Model, u.InputTokens, u.OutputTokens,
				u.CacheReadInputTokens, u.CacheCreationInputTokens, u.CacheCreation.OneHour),
		}
	}

	h.mu.Lock()
	for k, t := range newTurns { // last occurrence wins, as in readClaudeSession
		f.turns[k] = t
	}
	f.offset, f.size = consumed, fi.Size()
	if name != "" && h.names[f.dir] == "" {
		h.names[f.dir] = name
	}
	h.mu.Unlock()
}

// projRow is one project's spend, bucketed by day.
type projRow struct {
	Dir, Name                 string
	Today, Yday, D7, D30, All float64
	Sub7                      float64 // subagents' share of D7, in dollars
	Sessions                  int
}

// summary aggregates every scanned file into per-project rows (sorted by
// 7-day spend) and a per-day total for the last `days` days.
func (h *history) summary(now time.Time, days int) ([]projRow, []float64) {
	day := func(back int) string { return now.AddDate(0, 0, -back).Format("2006-01-02") }
	today, yday, d7, d30 := day(0), day(1), day(6), day(29)
	daily := make([]float64, days)
	dayIdx := map[string]int{}
	for i := 0; i < days; i++ {
		dayIdx[day(i)] = i
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	rows := map[string]*projRow{}
	for _, f := range h.files {
		r := rows[f.dir]
		if r == nil {
			r = &projRow{Dir: f.dir, Name: h.names[f.dir]}
			if r.Name == "" {
				r.Name = f.dir
			}
			rows[f.dir] = r
		}
		if !f.sub && len(f.turns) > 0 {
			r.Sessions++
		}
		for _, t := range f.turns {
			r.All += t.cost
			if t.day >= d30 {
				r.D30 += t.cost
			}
			if t.day >= d7 {
				r.D7 += t.cost
				if f.sub {
					r.Sub7 += t.cost
				}
			}
			if t.day == today {
				r.Today += t.cost
			}
			if t.day == yday {
				r.Yday += t.cost
			}
			if i, ok := dayIdx[t.day]; ok {
				daily[i] += t.cost
			}
		}
	}
	out := make([]projRow, 0, len(rows))
	for _, r := range rows {
		if r.All > 0 {
			out = append(out, *r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].D7 != out[j].D7 {
			return out[i].D7 > out[j].D7
		}
		return out[i].All > out[j].All
	})
	return out, daily
}

// todayFor returns a project directory's spend today (all its sessions,
// including cleared ones and their subagents).
func (h *history) todayFor(dir string, now time.Time) (float64, bool) {
	today := now.Format("2006-01-02")
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.scanned.IsZero() {
		return 0, false
	}
	sum := 0.0
	for _, f := range h.files {
		if f.dir != dir {
			continue
		}
		for _, t := range f.turns {
			if t.day == today {
				sum += t.cost
			}
		}
	}
	return sum, true
}
