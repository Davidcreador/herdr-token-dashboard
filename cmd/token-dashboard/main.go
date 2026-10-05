package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/lipgloss"
)

const pluginID = "dave.token-dashboard"
const opencodeServer = "http://127.0.0.1:4096"

// ── Lip Gloss styles ────────────────────────────────────────────────────────

var (
	cyan    = lipgloss.Color("#7dd3fc")
	green   = lipgloss.Color("#4ade80")
	yellow  = lipgloss.Color("#facc15")
	red     = lipgloss.Color("#f87171")
	purple  = lipgloss.Color("#c084fc")
	gray    = lipgloss.Color("#6b7280")
	dimGray = lipgloss.Color("#4b5563")
	white   = lipgloss.Color("#e5e7eb")
	blue    = lipgloss.Color("#60a5fa")
	orange  = lipgloss.Color("#fb923c")
	teal    = lipgloss.Color("#2dd4bf")

	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#0f172a")).
			Background(cyan).
			Padding(0, 2)
	subtitleStyle = lipgloss.NewStyle().
			Foreground(dimGray).
			Italic(true)

	// Table
	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(cyan)
	separatorStyle = lipgloss.NewStyle().
			Foreground(dimGray)
	totalSeparatorStyle = lipgloss.NewStyle().
				Foreground(yellow)
	totalStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(yellow)

	// Badges
	piBadge     = lipgloss.NewStyle().Foreground(purple).Bold(true)
	ocBadge     = lipgloss.NewStyle().Foreground(green).Bold(true)
	claudeBadge = lipgloss.NewStyle().Foreground(orange).Bold(true)
	codexBadge  = lipgloss.NewStyle().Foreground(teal).Bold(true)

	// Cost tiers
	costLow   = lipgloss.NewStyle().Foreground(green)
	costMid   = lipgloss.NewStyle().Foreground(yellow)
	costHigh  = lipgloss.NewStyle().Foreground(red).Bold(true)
	costNone  = lipgloss.NewStyle().Foreground(dimGray)
	costTotal = lipgloss.NewStyle().Foreground(yellow).Bold(true)

	// Status
	statusWorking = lipgloss.NewStyle().Foreground(yellow).Bold(true)
	statusIdle    = lipgloss.NewStyle().Foreground(green)
	statusDone    = lipgloss.NewStyle().Foreground(dimGray)
	statusBlocked = lipgloss.NewStyle().Foreground(red).Bold(true)
	statusUnknown = lipgloss.NewStyle().Foreground(dimGray)

	// Card styles
	cardBorderStyle = lipgloss.NewStyle().
			BorderLeft(true).
			BorderStyle(lipgloss.ThickBorder()).
			BorderForeground(dimGray).
			PaddingLeft(1)
	cardTitleStyle = lipgloss.NewStyle().Bold(true)
	labelStyle     = lipgloss.NewStyle().Foreground(gray)
	valueStyle     = lipgloss.NewStyle().Foreground(white).Bold(true)
	modelStyle     = lipgloss.NewStyle().Foreground(orange)
	providerStyle  = lipgloss.NewStyle().Foreground(teal)
	helpStyle      = lipgloss.NewStyle().
			Foreground(dimGray).
			Italic(true).
			MarginTop(1)
	errorStyle = lipgloss.NewStyle().Foreground(red).Bold(true)
)

// ── Data types ──────────────────────────────────────────────────────────────

type paneEntry struct {
	PaneID       string        `json:"pane_id"`
	Agent        string        `json:"agent,omitempty"`
	AgentStatus  string        `json:"agent_status,omitempty"`
	Label        string        `json:"label,omitempty"`
	TabID        string        `json:"tab_id,omitempty"`
	Cwd          string        `json:"cwd,omitempty"`
	Title        string        `json:"terminal_title_stripped,omitempty"`
	AgentSession *agentSession `json:"agent_session,omitempty"`
}

type agentSession struct {
	Agent  string `json:"agent"`
	Kind   string `json:"kind"`
	Source string `json:"source"`
	Value  string `json:"value"`
}

type paneListResponse struct {
	ID     string `json:"id"`
	Result struct {
		Panes []paneEntry `json:"panes"`
		Type  string      `json:"type"`
	} `json:"result"`
}

// tokenStats holds all data we can extract for one pane.
type tokenStats struct {
	PaneID      string
	Agent       string
	Status      string
	Cwd         string
	Cost        float64
	InputT      int
	OutputT     int
	ReasonT     int
	CacheR      int
	CacheW      int
	Source      string
	Model       string
	Provider    string
	Mode        string
	Messages    int
	Compactions int
	Subagents   int     // Claude Code: subagent transcripts folded into the totals
	SubCost     float64 // Claude Code: the subagents' share of Cost
	SessTitle   string  // the pane's terminal title (Claude Code sets it per conversation)
	ProjToday   float64 // the pane's project directory, today, all sessions (survives /clear)
	ProjTodayOK bool
	Started     time.Time
	LastAct     time.Time
	Duration    time.Duration
	Tools       map[string]int
	ToolTotal   int
	Title       string
	TabLabel    string
}

// OpenCode server API response types.
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

// ── Main ────────────────────────────────────────────────────────────────────

func main() {
	open := flag.Bool("open", false, "open the Herdr dashboard pane for this plugin")
	notify := flag.Bool("notify", false, "event-hook mode: read HERDR_PLUGIN_EVENT_JSON and send cost notification")
	flag.Parse()

	if *notify {
		runNotify()
		return
	}

	if *open {
		if err := openDashboard(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	p := tea.NewProgram(initialModel())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// ── Dashboard ───────────────────────────────────────────────────────────────

type model struct {
	width   int
	height  int
	stats   []tokenStats
	total   tokenStats
	updated time.Time
	err     string
	// prevStatus tracks the last known status per pane_id so the poll loop
	// can detect transitions to "done" and fire a Herdr notification.
	prevStatus map[string]string
	// offset is the first body line shown: the body (table + cards) scrolls
	// under a fixed header and help line.
	offset int
	// history switches the body to the per-project spend history.
	history bool
}

func initialModel() model {
	m := model{width: 80, height: 24, prevStatus: map[string]string{}}
	m.refresh()
	return m
}

func (m model) Init() tea.Cmd {
	hist.maybeScan()
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg { return tickMsg{} })
}

type tickMsg struct{}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tickMsg:
		hist.maybeScan()
		m.refresh()
		return m, tea.Tick(3*time.Second, func(time.Time) tea.Msg { return tickMsg{} })
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			m.offset -= 3
		case tea.MouseWheelDown:
			m.offset += 3
		}
	case tea.KeyPressMsg:
		page := m.bodyHeight() - 1
		if page < 1 {
			page = 1
		}
		switch msg.String() {
		case "ctrl+c", "q", "esc":
			return m, tea.Quit
		case "r":
			hist.maybeScan()
			m.refresh()
		case "h", "tab":
			m.history = !m.history
			m.offset = 0
		case "up", "k":
			m.offset--
		case "down", "j":
			m.offset++
		case "pgup", "b", "ctrl+u":
			m.offset -= page
		case "pgdown", "f", "space", "ctrl+d":
			m.offset += page
		case "home", "g":
			m.offset = 0
		case "end", "G":
			m.offset = 1 << 30 // clamped in View
		}
	}
	if m.offset < 0 {
		m.offset = 0
	}
	return m, nil
}

// bodyHeight is the number of screen lines available to the scrolling body:
// the terminal height minus the header (2 lines) and the help line (2 lines).
func (m model) bodyHeight() int {
	h := m.height - 4
	if h < 3 {
		h = 3
	}
	return h
}

func (m *model) refresh() {
	panes, err := fetchPanes()
	if err != nil {
		m.err = err.Error()
		return
	}
	m.err = ""
	m.stats = collectStats(panes)
	m.total = tokenStats{}
	for _, s := range m.stats {
		m.total.Cost += s.Cost
		m.total.InputT += s.InputT
		m.total.OutputT += s.OutputT
		m.total.ReasonT += s.ReasonT
		m.total.CacheR += s.CacheR
		m.total.CacheW += s.CacheW
		m.total.Messages += s.Messages
		m.total.ToolTotal += s.ToolTotal
		if s.LastAct.After(m.total.LastAct) {
			m.total.LastAct = s.LastAct
		}

		// Detect status transition to "done" and fire a notification.
		// This replaces the manifest [[events]] hook, which does not fire
		// for pane.agent_status_changed in Herdr 0.7.0. The dashboard poll
		// loop (every 3s) catches the transition instead.
		prev := m.prevStatus[s.PaneID]
		curr := s.Status
		if prev != "" && prev != "done" && curr == "done" && s.Cost > 0 {
			title := fmt.Sprintf("%s done: $%.2f", fallback(s.Agent, "agent"), s.Cost)
			body := fmt.Sprintf("Pane %s · msgs:%d · in:%s out:%s",
				shortPaneID(s.PaneID), s.Messages,
				fmtTokens(s.InputT), fmtTokens(s.OutputT))
			_ = sendNotification(title, body)
		}
		m.prevStatus[s.PaneID] = curr
	}
	m.updated = time.Now()
}

// ── Rendering helpers ───────────────────────────────────────────────────────

func costStyle(cost float64) lipgloss.Style {
	switch {
	case cost <= 0:
		return costNone
	case cost < 5:
		return costLow
	case cost < 25:
		return costMid
	default:
		return costHigh
	}
}

func statusDot(status string) string {
	switch status {
	case "working":
		return statusWorking.Render("●")
	case "idle":
		return statusIdle.Render("●")
	case "done":
		return statusDone.Render("●")
	case "blocked":
		return statusBlocked.Render("●")
	default:
		return statusUnknown.Render("●")
	}
}

func agentBadge(agent string) string {
	switch agent {
	case "pi":
		return piBadge.Render("pi")
	case "opencode":
		return ocBadge.Render("opencode")
	case "claude":
		return claudeBadge.Render("claude")
	case "codex":
		return codexBadge.Render("codex")
	default:
		return labelStyle.Render(fallback(agent, "—"))
	}
}

func fmtTokens(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}

func fmtDuration(d time.Duration) string {
	if d <= 0 {
		return "—"
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	if days > 0 {
		return fmt.Sprintf("%dd%dh", days, hours)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh%dm", hours, mins)
	}
	return fmt.Sprintf("%dm", mins)
}

func padRight(s string, width int) string {
	if lipgloss.Width(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-lipgloss.Width(s))
}

func trunc(s string, width int) string {
	if lipgloss.Width(s) <= width {
		return s
	}
	if width < 1 {
		return ""
	}
	r := []rune(s) // rune-safe: byte slicing split multi-byte characters
	for len(r) > 0 && lipgloss.Width(string(r))+1 > width {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}

// ── View ────────────────────────────────────────────────────────────────────

func (m model) View() tea.View {
	width := m.width
	if width < 40 {
		width = 40
	}

	// ── Header (fixed) ────────────────────────────────────────────────
	mode := "live"
	if m.history {
		mode = "history"
	}
	header := titleStyle.Render(" ◆ Token Dashboard ")
	sub := subtitleStyle.Render(
		fmt.Sprintf("  %s  ·  auto-refresh 3s  ·  %s", mode, m.updated.Format("15:04:05")),
	)

	// ── Body (scrolls) ────────────────────────────────────────────────
	b := m.renderBody(width)
	lines := strings.Split(strings.TrimRight(b, "\n"), "\n")
	bh := m.bodyHeight()
	maxOff := len(lines) - bh
	if maxOff < 0 {
		maxOff = 0
	}
	off := m.offset
	if off > maxOff {
		off = maxOff
	}
	end := off + bh
	if end > len(lines) {
		end = len(lines)
	}
	body := strings.Join(lines[off:end], "\n")
	if pad := bh - (end - off); pad > 0 {
		body += strings.Repeat("\n", pad)
	}

	// ── Help (fixed) ──────────────────────────────────────────────────
	pos := ""
	if maxOff > 0 {
		pos = fmt.Sprintf("  ·  lines %d–%d of %d", off+1, end, len(lines))
	}
	help := helpStyle.Render(m.helpText() + pos)

	view := tea.NewView(header + sub + "\n\n" + body + "\n" + help) // helpStyle carries its own MarginTop(1)
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	return view
}

// helpText is the fixed help line under the scrolling body.
func (m model) helpText() string {
	toggle := "h history"
	if m.history {
		toggle = "h live"
	}
	return "  ↑↓/jk scroll  ·  PgUp/PgDn  ·  g/G top/end  ·  " + toggle + "  ·  r refresh  ·  q/esc close"
}

// renderBody renders everything below the header; View scrolls it.
func (m model) renderBody(width int) string {
	var b strings.Builder
	switch {
	case m.err != "":
		b.WriteString(errorStyle.Render("  ⚠ "+m.err) + "\n")
	case m.history:
		b.WriteString(renderHistory(width))
	case len(m.stats) == 0:
		b.WriteString(labelStyle.Render("  No agent panes detected.") + "\n")
	default:
		b.WriteString(renderTable(m.stats, m.total, width))
		b.WriteString("\n")
		for _, s := range m.stats {
			b.WriteString(renderCard(s, width))
			b.WriteString("\n")
		}
		b.WriteString(renderSummary(m.total, len(m.stats), width))
	}
	return b.String()
}

// renderHistory renders per-project Claude Code spend from every transcript on
// disk (subagents included): today, yesterday, 7 and 30 days, all time.
func renderHistory(width int) string {
	var b strings.Builder
	if !hist.ready() {
		b.WriteString(labelStyle.Render("  Scanning ~/.claude/projects for the first time… (runs in the background)") + "\n")
		return b.String()
	}
	const days = 14
	rows, daily := hist.summary(time.Now(), days)

	wName, wNum, wSess := 40, 10, 6
	if avail := width - 4 - (6*wNum + wSess + 7); avail < wName {
		wName = avail
		if wName < 16 {
			wName = 16
		}
	}
	cols := []string{"TODAY", "YESTERDAY", "7 DAYS", "30 DAYS", "ALL", "SUB 7D"}
	hdr := []string{padRight("PROJECT", wName)}
	for _, c := range cols {
		hdr = append(hdr, padRight(c, wNum))
	}
	hdr = append(hdr, padRight("SESS", wSess))
	b.WriteString("  " + headerStyle.Render(strings.Join(hdr, " ")) + "\n")
	sepLen := wName + 6*wNum + wSess + 7
	b.WriteString("  " + separatorStyle.Render(strings.Repeat("─", sepLen)) + "\n")

	money := func(v float64) string {
		if v < 0.005 {
			return labelStyle.Render("—")
		}
		return costStyle(v).Render(fmt.Sprintf("$%.2f", v))
	}
	var tot projRow
	for _, r := range rows {
		share := labelStyle.Render("—")
		if r.D7 > 0 && r.Sub7 > 0 {
			share = costMid.Render(fmt.Sprintf("%.0f%%", 100*r.Sub7/r.D7))
		}
		parts := []string{
			padRight(trunc(r.Name, wName), wName),
			padRight(money(r.Today), wNum), padRight(money(r.Yday), wNum),
			padRight(money(r.D7), wNum), padRight(money(r.D30), wNum),
			padRight(money(r.All), wNum), padRight(share, wNum),
			padRight(labelStyle.Render(fmt.Sprintf("%d", r.Sessions)), wSess),
		}
		b.WriteString("  " + strings.Join(parts, " ") + "\n")
		tot.Today += r.Today
		tot.Yday += r.Yday
		tot.D7 += r.D7
		tot.D30 += r.D30
		tot.All += r.All
		tot.Sub7 += r.Sub7
		tot.Sessions += r.Sessions
	}
	b.WriteString("  " + totalSeparatorStyle.Render(strings.Repeat("═", sepLen)) + "\n")
	share := ""
	if tot.D7 > 0 {
		share = fmt.Sprintf("%.0f%%", 100*tot.Sub7/tot.D7)
	}
	totParts := []string{padRight("TOTAL", wName)}
	for _, v := range []float64{tot.Today, tot.Yday, tot.D7, tot.D30, tot.All} {
		totParts = append(totParts, padRight(costTotal.Render(fmt.Sprintf("$%.2f", v)), wNum))
	}
	totParts = append(totParts, padRight(share, wNum), padRight(fmt.Sprintf("%d", tot.Sessions), wSess))
	b.WriteString("  " + totalStyle.Render(strings.Join(totParts, " ")) + "\n\n")

	// Daily totals, newest first, with a bar scaled to the busiest day.
	b.WriteString("  " + headerStyle.Render(fmt.Sprintf("LAST %d DAYS", days)) + "\n")
	peak := 0.0
	for _, v := range daily {
		if v > peak {
			peak = v
		}
	}
	barW := width - 30
	if barW > 50 {
		barW = 50
	}
	now := time.Now()
	for i, v := range daily {
		n := 0
		if peak > 0 && barW > 0 {
			n = int(v / peak * float64(barW))
		}
		b.WriteString(fmt.Sprintf("  %s  %s %s\n",
			labelStyle.Render(now.AddDate(0, 0, -i).Format("Mon 01-02")),
			padRight(money(v), wNum),
			costMid.Render(strings.Repeat("█", n))))
	}
	b.WriteString("\n" + labelStyle.Render("  API list-price estimate (cache read 0.1×, write 1.25× / 2× for 1h TTL); a Max plan is not billed this way. Subagents included.") + "\n")
	return b.String()
}

// todayCell is the TODAY column: everything the pane's project directory spent
// today, across all its sessions (so it survives /clear) and their subagents.
func todayCell(s tokenStats) string {
	if s.Source != "claude" {
		return labelStyle.Render("—")
	}
	if !s.ProjTodayOK {
		return labelStyle.Render("…")
	}
	return costStyle(s.ProjToday).Render(fmt.Sprintf("$%.2f", s.ProjToday))
}

// totalToday sums TODAY over distinct project directories: two panes in the
// same directory share one TODAY figure and must not be counted twice.
func totalToday(stats []tokenStats) string {
	seen := map[string]bool{}
	sum, ok := 0.0, false
	for _, s := range stats {
		if s.Source != "claude" || !s.ProjTodayOK {
			continue
		}
		ok = true
		if d := mungeClaudePath(s.Cwd); !seen[d] {
			seen[d] = true
			sum += s.ProjToday
		}
	}
	if !ok {
		return ""
	}
	return costTotal.Render(fmt.Sprintf("$%.2f", sum))
}

// renderTable renders the summary table with responsive column widths.
func renderTable(stats []tokenStats, total tokenStats, width int) string {
	var b strings.Builder

	// Column widths — responsive to terminal width.
	avail := width - 4 // 2 indent + 2 padding
	wPane := 34
	wAgent := 9
	wStatus := 9
	wCost := 9
	wToday := 9
	wModel := 14
	wMsg := 6
	wTools := 6
	fixedW := wPane + wAgent + wStatus + wCost + wToday + wModel + wMsg + wTools + 7 // 7 spaces between
	if fixedW > avail && wPane-(fixedW-avail) >= 18 {
		// The SESSION column gives way first, down to 18 cells.
		wPane -= fixedW - avail
		fixedW = avail
	}
	if fixedW > avail {
		// Drop model and tools columns if too narrow.
		wModel = 0
		wTools = 0
		fixedW = wPane + wAgent + wStatus + wCost + wToday + wMsg + 5
		if fixedW > avail {
			wModel = 0
			wMsg = 0
			fixedW = wPane + wAgent + wStatus + wCost + wToday + 4
		}
	}

	sepLen := wPane + wAgent + wStatus + wCost + wToday + 1
	if wModel > 0 {
		sepLen += wModel
	}
	sepLen += wMsg
	if wTools > 0 {
		sepLen += wTools
	}
	sepLen += 7 // spaces between columns + indent
	if sepLen > avail {
		sepLen = avail
	}

	// Header row
	hdrParts := []string{
		padRight("SESSION", wPane),
		padRight("AGENT", wAgent),
		padRight("STATUS", wStatus),
		padRight("COST", wCost),
		padRight("TODAY", wToday),
	}
	if wModel > 0 {
		hdrParts = append(hdrParts, padRight("MODEL", wModel))
	}
	hdrParts = append(hdrParts, padRight("MSGS", wMsg))
	if wTools > 0 {
		hdrParts = append(hdrParts, padRight("TOOLS", wTools))
	}
	b.WriteString("  " + headerStyle.Render(strings.Join(hdrParts, " ")) + "\n")
	b.WriteString("  " + separatorStyle.Render(strings.Repeat("─", sepLen)) + "\n")

	// Data rows
	for _, s := range stats {
		costStr := "—"
		costS := costStyle(s.Cost)
		if s.Cost > 0 {
			costStr = fmt.Sprintf("$%.2f", s.Cost)
		}

		statusStr := statusDot(s.Status) + " " + fallback(s.Status, "—")

		msgStr := "—"
		if s.Messages > 0 {
			msgStr = fmt.Sprintf("%d", s.Messages)
		}
		toolStr := "—"
		if s.ToolTotal > 0 {
			toolStr = fmt.Sprintf("%d", s.ToolTotal)
		}

		rowParts := []string{
			padRight(trunc(paneDisplay(s), wPane), wPane),
			padRight(agentBadge(s.Agent), wAgent),
			padRight(statusStr, wStatus),
			padRight(costS.Render(costStr), wCost),
			padRight(todayCell(s), wToday),
		}
		if wModel > 0 {
			modelStr := fallback(s.Model, "—")
			rowParts = append(rowParts, padRight(modelStyle.Render(trunc(modelStr, wModel)), wModel))
		}
		rowParts = append(rowParts, padRight(labelStyle.Render(msgStr), wMsg))
		if wTools > 0 {
			rowParts = append(rowParts, padRight(labelStyle.Render(toolStr), wTools))
		}
		b.WriteString("  " + strings.Join(rowParts, " ") + "\n")
	}

	// Totals row
	b.WriteString("  " + totalSeparatorStyle.Render(strings.Repeat("═", sepLen)) + "\n")
	totMsg := "—"
	if total.Messages > 0 {
		totMsg = fmt.Sprintf("%d", total.Messages)
	}
	totTool := "—"
	if total.ToolTotal > 0 {
		totTool = fmt.Sprintf("%d", total.ToolTotal)
	}
	totParts := []string{
		padRight("TOTAL", wPane),
		padRight("", wAgent),
		padRight("", wStatus),
		padRight(costTotal.Render(fmt.Sprintf("$%.2f", total.Cost)), wCost),
		padRight(totalToday(stats), wToday),
	}
	if wModel > 0 {
		totParts = append(totParts, padRight("", wModel))
	}
	totParts = append(totParts, padRight(valueStyle.Render(totMsg), wMsg))
	if wTools > 0 {
		totParts = append(totParts, padRight(valueStyle.Render(totTool), wTools))
	}
	b.WriteString("  " + totalStyle.Render(strings.Join(totParts, " ")) + "\n")

	return b.String()
}

// renderCard renders a per-agent detail card with session metadata.
func renderCard(s tokenStats, width int) string {
	var b strings.Builder

	// Agent header with badge + status + model
	badge := agentBadge(s.Agent)
	dot := statusDot(s.Status)
	statusText := fallback(s.Status, "—")

	headerLine := fmt.Sprintf("%s %s %s", badge, dot, statusText)
	if label := paneDisplay(s); label != "" {
		headerLine += "  " + valueStyle.Render(label)
	}
	if s.Model != "" {
		headerLine += "  " + modelStyle.Render(s.Model)
	}
	if s.Provider != "" {
		headerLine += " " + providerStyle.Render("("+s.Provider+")")
	}

	// Card with left border
	var inner strings.Builder
	inner.WriteString(headerLine + "\n")

	// Pane id — the table column may be showing the tab label instead.
	inner.WriteString(fmt.Sprintf("  %s %s\n",
		labelStyle.Render("pane:"),
		valueStyle.Render(shortPaneID(s.PaneID)),
	))

	// Title
	if s.Title != "" {
		inner.WriteString(fmt.Sprintf("  %s %s\n",
			labelStyle.Render("title:"),
			valueStyle.Render(trunc(s.Title, width-12)),
		))
	}

	// Cwd
	if s.Cwd != "" {
		cwd := s.Cwd
		if len(cwd) > width-12 {
			cwd = "..." + cwd[len(cwd)-(width-15):]
		}
		inner.WriteString(fmt.Sprintf("  %s %s\n", labelStyle.Render("cwd:"), valueStyle.Render(cwd)))
	}

	// Duration
	if s.Duration > 0 {
		inner.WriteString(fmt.Sprintf("  %s %s", labelStyle.Render("session:"), valueStyle.Render(fmtDuration(s.Duration))))
		if !s.LastAct.IsZero() {
			inner.WriteString(fmt.Sprintf("  %s %s", labelStyle.Render("last:"), valueStyle.Render(s.LastAct.Format("15:04"))))
		}
		inner.WriteString("\n")
	}

	// Token breakdown — only if we have real data
	if s.InputT > 0 || s.OutputT > 0 || s.CacheR > 0 {
		inner.WriteString(fmt.Sprintf("  %s in:%s out:%s reason:%s cache_r:%s cache_w:%s\n",
			labelStyle.Render("tokens:"),
			valueStyle.Render(fmtTokens(s.InputT)),
			valueStyle.Render(fmtTokens(s.OutputT)),
			valueStyle.Render(fmtTokens(s.ReasonT)),
			costLow.Render(fmtTokens(s.CacheR)),
			valueStyle.Render(fmtTokens(s.CacheW)),
		))
	}

	// Messages + compactions
	if s.Messages > 0 || s.Compactions > 0 {
		parts := []string{}
		if s.Messages > 0 {
			parts = append(parts, labelStyle.Render("msgs: ")+valueStyle.Render(fmt.Sprintf("%d", s.Messages)))
		}
		if s.Compactions > 0 {
			parts = append(parts, labelStyle.Render("compactions: ")+costMid.Render(fmt.Sprintf("%d", s.Compactions)))
		}
		if s.Subagents > 0 {
			share := ""
			if s.Cost > 0 {
				share = fmt.Sprintf(" (%.0f%% of cost)", 100*s.SubCost/s.Cost)
			}
			parts = append(parts, labelStyle.Render("subagents: ")+costMid.Render(fmt.Sprintf("%d · $%.2f%s", s.Subagents, s.SubCost, share)))
		}
		inner.WriteString("  " + strings.Join(parts, "  ") + "\n")
	}

	// Tool breakdown — wrap across multiple lines if the list is long.
	if s.ToolTotal > 0 && len(s.Tools) > 0 {
		var tools []string
		for _, name := range sortedToolNames(s.Tools) {
			tools = append(tools, fmt.Sprintf("%s×%d", name, s.Tools[name]))
		}
		toolLine := strings.Join(tools, "  ")
		maxW := width - 14
		if maxW < 20 {
			maxW = 20
		}
		if lipgloss.Width(toolLine) <= maxW {
			inner.WriteString(fmt.Sprintf("  %s %s\n",
				labelStyle.Render("tools:"),
				valueStyle.Render(toolLine),
			))
		} else {
			// Wrap tool list across lines.
			var line strings.Builder
			line.WriteString("  " + labelStyle.Render("tools:") + " ")
			first := true
			curW := 0
			for _, t := range tools {
				tw := lipgloss.Width(t) + 2
				if !first && curW+tw > maxW {
					inner.WriteString(line.String() + "\n")
					line.Reset()
					line.WriteString("    ")
					curW = 0
				} else if !first {
					line.WriteString("  ")
					curW += 2
				}
				line.WriteString(valueStyle.Render(t))
				curW += lipgloss.Width(t)
				first = false
			}
			if line.Len() > 0 {
				inner.WriteString(line.String() + "\n")
			}
		}
	}

	// If no data at all beyond cwd, show a hint
	if s.InputT == 0 && s.OutputT == 0 && s.CacheR == 0 && s.Messages == 0 && s.ToolTotal == 0 && s.Cost == 0 {
		inner.WriteString("  " + labelStyle.Render("(no token data available)") + "\n")
	}

	card := cardBorderStyle.Render(inner.String())
	b.WriteString(card + "\n")
	return b.String()
}

// renderSummary renders the bottom summary line.
func renderSummary(total tokenStats, paneCount int, width int) string {
	parts := []string{
		labelStyle.Render("panes: ") + valueStyle.Render(fmt.Sprintf("%d", paneCount)),
		labelStyle.Render("cost: ") + costTotal.Render(fmt.Sprintf("$%.2f", total.Cost)),
	}
	if total.InputT > 0 {
		parts = append(parts, labelStyle.Render("in: ")+valueStyle.Render(fmtTokens(total.InputT)))
	}
	if total.OutputT > 0 {
		parts = append(parts, labelStyle.Render("out: ")+valueStyle.Render(fmtTokens(total.OutputT)))
	}
	if total.CacheR > 0 {
		parts = append(parts, labelStyle.Render("cache: ")+costLow.Render(fmtTokens(total.CacheR)))
	}
	if total.Messages > 0 {
		parts = append(parts, labelStyle.Render("msgs: ")+valueStyle.Render(fmt.Sprintf("%d", total.Messages)))
	}
	if total.ToolTotal > 0 {
		parts = append(parts, labelStyle.Render("tools: ")+valueStyle.Render(fmt.Sprintf("%d", total.ToolTotal)))
	}
	return "  " + strings.Join(parts, "  ") + "\n"
}

func sortedToolNames(tools map[string]int) []string {
	type kv struct {
		k string
		v int
	}
	var list []kv
	for k, v := range tools {
		list = append(list, kv{k, v})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].v > list[j].v })
	names := make([]string, len(list))
	for i, e := range list {
		names[i] = e.k
	}
	return names
}

// ── Notify mode ─────────────────────────────────────────────────────────────

type eventPayload struct {
	Event       string `json:"event"`
	PaneID      string `json:"pane_id"`
	AgentStatus string `json:"agent_status"`
	Agent       string `json:"agent,omitempty"`
}

func runNotify() {
	// Debug log to plugin state dir so we can see what Herdr actually sends.
	debugLog("notify invoked")

	raw := os.Getenv("HERDR_PLUGIN_EVENT_JSON")
	debugLog("HERDR_PLUGIN_EVENT_JSON=" + raw)
	if raw == "" {
		debugLog("no event JSON, exiting")
		return
	}

	var ev eventPayload
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		debugLog(fmt.Sprintf("parse error: %v", err))
		return
	}
	debugLog(fmt.Sprintf("event=%s pane=%s status=%s agent=%s", ev.Event, ev.PaneID, ev.AgentStatus, ev.Agent))

	if ev.AgentStatus != "done" {
		debugLog("status != done, skipping")
		return
	}
	debugLog("status is done, looking up pane...")

	panes, err := fetchPanes()
	if err != nil {
		debugLog(fmt.Sprintf("fetchPanes error: %v", err))
		return
	}
	debugLog(fmt.Sprintf("found %d panes", len(panes)))

	for _, p := range panes {
		debugLog(fmt.Sprintf("checking pane %s vs event pane %s", p.PaneID, ev.PaneID))
		if p.PaneID == ev.PaneID && p.AgentSession != nil {
			debugLog("matched pane, extracting stats...")
			stats := extractStats(p)
			debugLog(fmt.Sprintf("cost=%.2f msgs=%d", stats.Cost, stats.Messages))
			if stats.Cost > 0 {
				title := fmt.Sprintf("%s done: $%.2f", fallback(stats.Agent, "agent"), stats.Cost)
				body := fmt.Sprintf("Pane %s · msgs:%d · in:%s out:%s",
					shortPaneID(stats.PaneID), stats.Messages,
					fmtTokens(stats.InputT), fmtTokens(stats.OutputT))
				debugLog("sending notification: " + title)
				if err := sendNotification(title, body); err != nil {
					debugLog(fmt.Sprintf("notification error: %v", err))
				} else {
					debugLog("notification sent successfully")
				}
			} else {
				debugLog("cost is 0, not notifying")
			}
			return
		}
	}
	debugLog("no matching pane found")
}

func sendNotification(title, body string) error {
	herdr := os.Getenv("HERDR_BIN_PATH")
	if herdr == "" {
		herdr = "herdr"
	}
	cmd := exec.Command(herdr, "notification", "show", title,
		"--body", body, "--position", "top-right", "--sound", "done")
	return cmd.Run()
}

// ── Open dashboard ──────────────────────────────────────────────────────────

func openDashboard() error {
	herdr := os.Getenv("HERDR_BIN_PATH")
	if herdr == "" {
		herdr = "herdr"
	}
	cmd := exec.Command(herdr, "plugin", "pane", "open",
		"--plugin", pluginID, "--entrypoint", "dashboard", "--placement", "tab")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// ── Data collection ─────────────────────────────────────────────────────────

func fetchPanes() ([]paneEntry, error) {
	herdr := os.Getenv("HERDR_BIN_PATH")
	if herdr == "" {
		herdr = "herdr"
	}
	cmd := exec.Command(herdr, "pane", "list")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("herdr pane list: %w", err)
	}

	var resp paneListResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("parse pane list: %w", err)
	}
	return resp.Result.Panes, nil
}

// fetchTabLabels maps tab_id -> tab label. Best effort: on any failure it
// returns an empty map and the PANE column falls back to the short pane id.
func fetchTabLabels() map[string]string {
	labels := map[string]string{}
	herdr := os.Getenv("HERDR_BIN_PATH")
	if herdr == "" {
		herdr = "herdr"
	}
	cmd := exec.Command(herdr, "tab", "list")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return labels
	}
	var resp struct {
		Result struct {
			Tabs []struct {
				TabID string `json:"tab_id"`
				Label string `json:"label"`
			} `json:"tabs"`
		} `json:"result"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		return labels
	}
	for _, t := range resp.Result.Tabs {
		if t.Label != "" {
			labels[t.TabID] = t.Label
		}
	}
	return labels
}

// paneDisplay is the SESSION column value. A tab the user has named keeps its
// label; Herdr's default tab labels are just numbers ("1", "2", …) and identify
// nothing, so those panes show the project (from the cwd) and the session's
// terminal title, which Claude Code sets per conversation.
func paneDisplay(s tokenStats) string {
	if s.TabLabel != "" && !isDefaultTabLabel(s.TabLabel) {
		return s.TabLabel
	}
	name := projectName(s.Cwd)
	switch {
	case name != "" && s.SessTitle != "":
		return name + " · " + s.SessTitle
	case name != "":
		return name
	case s.SessTitle != "":
		return s.SessTitle
	case s.TabLabel != "":
		return s.TabLabel
	}
	return shortPaneID(s.PaneID)
}

// isDefaultTabLabel reports whether a tab label is Herdr's numeric default.
func isDefaultTabLabel(l string) bool {
	for _, r := range l {
		if r < '0' || r > '9' {
			return false
		}
	}
	return l != ""
}

// projectName turns a working directory into a short, recognisable name:
// a Claude Code worktree (<repo>/.claude/worktrees/<name>) and a Herdr
// worktree (~/.herdr/worktrees/<repo>/<name>) both become "<repo>/<name>";
// anything else is its last path component.
func projectName(cwd string) string {
	cwd = strings.TrimRight(cwd, "/")
	if i := strings.Index(cwd, "/.claude/worktrees/"); i >= 0 {
		return filepath.Base(cwd[:i]) + "/" + cwd[i+len("/.claude/worktrees/"):]
	}
	if i := strings.Index(cwd, "/.herdr/worktrees/"); i >= 0 {
		return cwd[i+len("/.herdr/worktrees/"):]
	}
	if cwd == "" {
		return ""
	}
	return filepath.Base(cwd)
}

func collectStats(panes []paneEntry) []tokenStats {
	var stats []tokenStats
	tabLabels := fetchTabLabels()
	for _, p := range panes {
		if p.AgentSession == nil {
			continue
		}
		s := extractStats(p)
		s.Status = p.AgentStatus
		s.Cwd = p.Cwd
		s.TabLabel = tabLabels[p.TabID]
		s.SessTitle = p.Title
		if s.Source == "claude" {
			s.ProjToday, s.ProjTodayOK = hist.todayFor(mungeClaudePath(p.Cwd), time.Now())
		}
		stats = append(stats, s)
	}
	sort.Slice(stats, func(i, j int) bool { return stats[i].Cost > stats[j].Cost })
	return stats
}

func extractStats(p paneEntry) tokenStats {
	s := tokenStats{
		PaneID: p.PaneID,
		Agent:  p.Agent,
		Tools:  map[string]int{},
	}

	if p.AgentSession == nil {
		return s
	}

	switch p.AgentSession.Source {
	case "herdr:pi":
		s.Source = "pi"
		readPiSession(p.AgentSession.Value, &s)
	case "herdr:opencode":
		s.Source = "opencode"
		readOpenCodeLive(p.AgentSession.Value, &s)
	case "herdr:claude", "claude":
		s.Source = "claude"
		readClaudeSession(p.AgentSession.Value, p.Cwd, &s)
	case "herdr:codex", "codex":
		s.Source = "codex"
		readCodexSession(p.AgentSession.Value, &s)
	default:
		if strings.HasSuffix(p.AgentSession.Value, ".jsonl") {
			s.Source = "pi"
			readPiSession(p.AgentSession.Value, &s)
		}
	}

	return s
}

// readPiSession reads a Pi session JSONL and extracts all available metadata.
func readPiSession(path string, s *tokenStats) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}

	var firstTS, lastTS time.Time
	seenTools := map[string]bool{}

	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var entry struct {
			Type      string `json:"type"`
			Timestamp string `json:"timestamp"`
			ModelID   string `json:"modelId"`
			Provider  string `json:"provider"`
			Data      struct {
				SessionCostUsd float64 `json:"sessionCostUsd"`
				PinnedModelKey string  `json:"pinnedModelKey"`
				Phase          string  `json:"phase"`
			} `json:"data"`
			Message struct {
				Role     string          `json:"role"`
				Model    string          `json:"model"`
				Provider string          `json:"provider"`
				Content  json.RawMessage `json:"content"`
				Usage    struct {
					Input      int `json:"input"`
					Output     int `json:"output"`
					CacheRead  int `json:"cacheRead"`
					CacheWrite int `json:"cacheWrite"`
					Cost       struct {
						Total float64 `json:"total"`
					} `json:"cost"`
				} `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &entry) != nil {
			continue
		}

		if entry.Timestamp != "" {
			if ts, err := time.Parse(time.RFC3339Nano, entry.Timestamp); err == nil {
				if firstTS.IsZero() {
					firstTS = ts
				}
				lastTS = ts
			}
		}

		switch entry.Type {
		case "model_change":
			if entry.ModelID != "" {
				s.Model = entry.ModelID
			}
			if entry.Provider != "" {
				s.Provider = entry.Provider
			}
		case "custom":
			if entry.Data.SessionCostUsd > 0 {
				s.Cost = entry.Data.SessionCostUsd
			}
			if entry.Data.PinnedModelKey != "" {
				s.Model = entry.Data.PinnedModelKey
			}
		case "message":
			if entry.Message.Role == "assistant" {
				s.Messages++
				if entry.Message.Model != "" {
					s.Model = entry.Message.Model
				}
				if entry.Message.Provider != "" {
					s.Provider = entry.Message.Provider
				}
				s.InputT += entry.Message.Usage.Input
				s.OutputT += entry.Message.Usage.Output
				s.CacheR += entry.Message.Usage.CacheRead
				s.CacheW += entry.Message.Usage.CacheWrite
				s.Cost += entry.Message.Usage.Cost.Total

				var blocks []struct {
					Type string `json:"type"`
					ID   string `json:"id"`
					Name string `json:"name"`
				}
				if len(entry.Message.Content) > 0 && json.Unmarshal(entry.Message.Content, &blocks) == nil {
					for _, blk := range blocks {
						if blk.Type != "toolCall" || blk.Name == "" {
							continue
						}
						if blk.ID != "" {
							if seenTools[blk.ID] {
								continue
							}
							seenTools[blk.ID] = true
						}
						s.Tools[blk.Name]++
						s.ToolTotal++
					}
				}
			}
		case "compaction":
			s.Compactions++
		}
	}

	s.Started = firstTS
	s.LastAct = lastTS
	if !firstTS.IsZero() && !lastTS.IsZero() {
		s.Duration = lastTS.Sub(firstTS)
	}
}

// ── Claude Code sessions ────────────────────────────────────────────────────

// claudePricing maps a model-id substring to estimated Anthropic per-MTok
// USD rates. Prices are ESTIMATES based on public list pricing — update the
// rates here when Anthropic pricing changes. Matching is by substring,
// longest match first, so more specific entries (e.g. "sonnet-4-5") win over
// broader ones ("sonnet-4"). Cache writes are billed at 1.25× input (2× for
// the 1-hour TTL, see claudeCostTTL). Cache
// read rates are 0.1× by default, with exceptions in claudeCacheReadMultiplier.
// Unknown models get no cost estimate (tokens are still shown).
var claudePricing = []struct {
	substr string
	in     float64 // USD per MTok input
	out    float64 // USD per MTok output
}{
	{"fable-5-1", 10, 50},
	{"mythos-5-1", 10, 50},
	{"fable-5", 10, 50},
	{"mythos-5", 10, 50},
	{"opus-5-5", 4, 20},
	{"sonnet-5-5", 2, 10},
	{"opus-5", 5, 25},
	{"sonnet-5", 2, 10},
	// Opus 4.5 through 4.8 are $5/$25 — they must be listed explicitly, because
	// the bare "opus-4" entry below substring-matches them and would otherwise
	// price them at the legacy Opus 4 / 4.1 rate.
	{"opus-4-8", 5, 25},
	{"opus-4-7", 5, 25},
	{"opus-4-6", 5, 25},
	{"opus-4-5", 5, 25},
	{"opus-4", 15, 75},
	{"sonnet-4-6", 3, 15},
	{"sonnet-4-5", 3, 15},
	{"sonnet-4", 3, 15},
	{"haiku-4-5", 1, 5},
}

// claudeRates returns the estimated per-MTok rates for a model id, matching
// pricing-table substrings longest-first. ok is false for unknown models.
func claudeRates(model string) (in, out float64, ok bool) {
	best := -1
	for _, p := range claudePricing {
		if strings.Contains(model, p.substr) && len(p.substr) > best {
			best = len(p.substr)
			in, out = p.in, p.out
			ok = true
		}
	}
	return in, out, ok
}

// blendedCost applies the default 0.1x cache-read and 1.25x cache-write
// multipliers. Models with a different published cache-read rate pass it to
// blendedCostWithCacheRead.
func blendedCost(in, out float64, input, output, cacheRead, cacheWrite int) float64 {
	return blendedCostWithCacheRead(in, out, input, output, cacheRead, cacheWrite, 0.1)
}

func blendedCostWithCacheRead(in, out float64, input, output, cacheRead, cacheWrite int, cacheReadMultiplier float64) float64 {
	return (float64(input)*in +
		float64(output)*out +
		float64(cacheRead)*cacheReadMultiplier*in +
		float64(cacheWrite)*1.25*in) / 1_000_000
}

// claudeCost estimates the USD cost of one assistant turn.
func claudeCost(model string, input, output, cacheRead, cacheWrite int) float64 {
	return claudeCostTTL(model, input, output, cacheRead, cacheWrite, 0)
}

// claudeCostTTL is claudeCost with the 1-hour-TTL share of the cache writes
// (usage.cache_creation.ephemeral_1h_input_tokens) priced at 2x input instead
// of 1.25x, per Anthropic's prompt-caching pricing. Claude Code main sessions
// typically write with the 1h TTL; subagents with the 5m TTL.
func claudeCostTTL(model string, input, output, cacheRead, cacheWrite, cacheWrite1h int) float64 {
	in, out, ok := claudeRates(model)
	if !ok {
		return 0
	}
	if cacheWrite1h > cacheWrite {
		cacheWrite1h = cacheWrite
	}
	return blendedCostWithCacheRead(in, out, input, output, cacheRead, cacheWrite-cacheWrite1h, claudeCacheReadMultiplier(model)) +
		float64(cacheWrite1h)*2.0*in/1_000_000
}

// Anthropic's newest models have model-specific cache-hit prices.
func claudeCacheReadMultiplier(model string) float64 {
	if strings.Contains(model, "fable-5-1") || strings.Contains(model, "mythos-5-1") {
		return 0.025
	}
	if strings.Contains(model, "opus-5-5") {
		return 0.05
	}
	return 0.1
}

// claudeProjectsRoot returns the Claude Code projects directory
// (~/.claude/projects). A variable so tests can point it at a fixture dir.
var claudeProjectsRoot = func() string {
	h := homeDir()
	if h == "" {
		return ""
	}
	return filepath.Join(h, ".claude", "projects")
}

// mungeClaudePath converts a working directory into the directory name
// Claude Code uses under ~/.claude/projects: every character outside
// [A-Za-z0-9-] is replaced by '-'.
func mungeClaudePath(p string) string {
	var b strings.Builder
	for _, r := range p {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}

// claudeSessionPath locates the transcript for a Claude Code session. The
// munged-cwd path is tried first; if it misses (e.g. the pane cwd changed
// after launch), fall back to globbing every project dir — session UUIDs
// are unique.
func claudeSessionPath(sessionID, cwd string) string {
	root := claudeProjectsRoot()
	if root == "" || sessionID == "" {
		return ""
	}
	if cwd != "" {
		p := filepath.Join(root, mungeClaudePath(cwd), sessionID+".jsonl")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	matches, err := filepath.Glob(filepath.Join(root, "*", sessionID+".jsonl"))
	if err != nil || len(matches) == 0 {
		return ""
	}
	return matches[0]
}

// readClaudeSession reads a Claude Code session transcript JSONL and
// extracts tokens, estimated cost, model, message count, tool calls, and
// session duration. Streaming and retries can repeat records for the same
// assistant message, so usage is aggregated per (message.id, requestId)
// pair — each unique pair counts once, last occurrence wins.
func readClaudeSession(sessionID, cwd string, s *tokenStats) {
	path := claudeSessionPath(sessionID, cwd)
	if path == "" {
		debugLog("claude transcript not found for session " + sessionID)
		return
	}
	// Subagents (Task/Agent tool) write their own transcripts beside the
	// session, in <session-id>/subagents/agent-*.jsonl. They spend the same
	// plan, so they are folded into the totals; the session's timing still
	// comes from the main transcript only.
	files := []string{path}
	subs, _ := filepath.Glob(filepath.Join(strings.TrimSuffix(path, ".jsonl"), "subagents", "*.jsonl"))
	files = append(files, subs...)

	type claudeTurn struct {
		model                         string
		input, output, cacheR, cacheW int
		cacheW1h                      int
		sub                           bool
	}
	turns := map[string]claudeTurn{}
	seenTools := map[string]bool{}
	var firstTS, lastTS time.Time

	for fi, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			if fi == 0 {
				return
			}
			continue
		}
		isSub := fi > 0

		for _, line := range bytes.Split(data, []byte("\n")) {
			if len(line) == 0 {
				continue
			}
			var entry struct {
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
					Content json.RawMessage `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal(line, &entry) != nil {
				continue
			}

			if entry.Timestamp != "" && !isSub {
				if ts, err := time.Parse(time.RFC3339Nano, entry.Timestamp); err == nil {
					if firstTS.IsZero() {
						firstTS = ts
					}
					lastTS = ts
				}
			}

			if entry.Type != "assistant" || entry.Message.ID == "" {
				continue
			}

			if entry.Message.Model != "" {
				s.Model = entry.Message.Model
				s.Provider = "anthropic"
			}

			turns[entry.Message.ID+"\x00"+entry.RequestID] = claudeTurn{
				model:    entry.Message.Model,
				input:    entry.Message.Usage.InputTokens,
				output:   entry.Message.Usage.OutputTokens,
				cacheR:   entry.Message.Usage.CacheReadInputTokens,
				cacheW:   entry.Message.Usage.CacheCreationInputTokens,
				sub:      isSub,
				cacheW1h: entry.Message.Usage.CacheCreation.OneHour,
			}

			// Tool calls appear as tool_use content blocks. Blocks carry unique
			// ids, so repeated records for the same message don't double-count.
			var blocks []struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			}
			if len(entry.Message.Content) == 0 || json.Unmarshal(entry.Message.Content, &blocks) != nil {
				continue
			}
			for _, blk := range blocks {
				if blk.Type != "tool_use" || blk.Name == "" {
					continue
				}
				if blk.ID != "" {
					if seenTools[blk.ID] {
						continue
					}
					seenTools[blk.ID] = true
				}
				s.Tools[blk.Name]++
				s.ToolTotal++
			}
		}

	}

	s.Subagents = len(subs)
	for _, t := range turns {
		s.InputT += t.input
		s.OutputT += t.output
		s.CacheR += t.cacheR
		s.CacheW += t.cacheW
		c := claudeCostTTL(t.model, t.input, t.output, t.cacheR, t.cacheW, t.cacheW1h)
		s.Cost += c
		if t.sub {
			s.SubCost += c
		}
	}
	s.Messages = len(turns)

	s.Started = firstTS
	s.LastAct = lastTS
	if !firstTS.IsZero() && !lastTS.IsZero() {
		s.Duration = lastTS.Sub(firstTS)
	}
}

// readOpenCodeLive queries the OpenCode server API for live session data.
// This gets cost, tokens, model, provider, message count, and tool calls
// for active sessions that haven't been persisted to disk yet.
func readOpenCodeLive(sessionID string, s *tokenStats) {
	// Fetch session summary
	resp, err := http.Get(opencodeServer + "/session/" + sessionID)
	if err != nil {
		// Fallback to disk-based reading for completed sessions.
		readOpenCodeDisk(sessionID, s)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		readOpenCodeDisk(sessionID, s)
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
	s.Title = sess.Title

	if sess.Time.Created > 0 {
		s.Started = time.UnixMilli(sess.Time.Created)
	}
	if sess.Time.Updated > 0 {
		s.LastAct = time.UnixMilli(sess.Time.Updated)
	}
	if !s.Started.IsZero() && !s.LastAct.IsZero() {
		s.Duration = s.LastAct.Sub(s.Started)
	}

	// Fetch messages for assistant turn count + tool calls.
	readOpenCodeMessages(sessionID, s)
}

// readOpenCodeMessages fetches the message list from the OpenCode server API
// and counts assistant turns and tool calls.
func readOpenCodeMessages(sessionID string, s *tokenStats) {
	resp, err := http.Get(opencodeServer + "/session/" + sessionID + "/message")
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
// Used as a fallback when the server API is unavailable.
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

func homeDir() string {
	u, err := user.Current()
	if err != nil {
		return ""
	}
	return u.HomeDir
}

// ── Helpers ─────────────────────────────────────────────────────────────────

// debugLog writes a line to a debug log file.
// Uses HERDR_PLUGIN_STATE_DIR when available, falls back to /tmp.
func debugLog(msg string) {
	logPath := "/tmp/herdr-token-dashboard-notify.log"
	if stateDir := os.Getenv("HERDR_PLUGIN_STATE_DIR"); stateDir != "" {
		logPath = filepath.Join(stateDir, "notify-debug.log")
	}
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format("15:04:05.000"), msg)
}

func shortPaneID(id string) string {
	parts := strings.SplitN(id, ":", 2)
	if len(parts) == 2 {
		w := parts[0]
		if len(w) > 7 {
			w = w[:7]
		}
		return w + ":" + parts[1]
	}
	return id
}

func fallback(value, fb string) string {
	if value == "" {
		return fb
	}
	return value
}

// init runs before main; not used but ensures file compiles.
