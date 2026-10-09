package ui

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"lilbuddy/internal/github"
	"lilbuddy/internal/state"
)

type Model struct {
	client        *github.Client
	store         *state.Store
	viewer        string
	interval      time.Duration
	ctx           context.Context
	items         []github.Item
	allItems      []github.Item
	showDrafts    bool
	warnings      []string
	cursor        int
	width         int
	height        int
	buddyFrame    int
	spinnerFrame  int
	spinning      bool
	loading       bool
	opening       bool
	revision      int
	status        string
	err           error
	updated       time.Time
	cached        bool
	authenticated bool
	connect       Connect
	// Injectable so tests never launch a browser.
	open func(context.Context, string) error
}

type loaded struct {
	result   github.Result
	err      error
	revision int
	client   *github.Client
	viewer   string
}

type opened struct {
	item   github.Item
	err    error
	viewer string
}

type tick struct{}

type spinnerTick struct{}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

type Connect func(context.Context) (*github.Client, string, error)

func New(ctx context.Context, client *github.Client, store *state.Store, viewer string, interval time.Duration) Model {
	m := Model{ctx: ctx, client: client, store: store, viewer: viewer, interval: interval, width: 100, height: 24, loading: true, spinning: true, open: OpenBrowser, authenticated: client != nil && viewer != ""}
	m.restoreCache()
	return m
}

// NewStartup performs no authentication or API calls before showing the cache.
func NewStartup(ctx context.Context, store *state.Store, interval time.Duration, connect Connect) Model {
	m := New(ctx, nil, store, store.LastViewer, interval)
	m.connect = connect
	return m
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.load(), m.nextTick(), nextSpinnerTick())
}

func nextSpinnerTick() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return spinnerTick{} })
}

// startLoading keeps a single spinner loop alive for the duration of a refresh.
func (m *Model) startLoading() tea.Cmd {
	m.loading = true
	if m.spinning {
		return nil
	}
	m.spinning = true
	return nextSpinnerTick()
}

func (m Model) load() tea.Cmd {
	entries := make(map[string]state.Entry, len(m.store.Items))
	for key, value := range m.store.Items {
		entries[key] = value
	}
	revision := m.revision
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 3*time.Minute)
		defer cancel()
		client, viewer := m.client, m.viewer
		if !m.authenticated {
			if m.connect == nil {
				return loaded{err: fmt.Errorf("GitHub authentication is not configured"), revision: revision}
			}
			var err error
			client, viewer, err = m.connect(ctx)
			if err != nil {
				return loaded{err: err, revision: revision}
			}
		}
		result, err := client.Discover(ctx, viewer)
		if err == nil {
			result = client.Filter(ctx, result, viewer, entries)
			if ctx.Err() != nil {
				err = ctx.Err()
			}
		}
		return loaded{result: result, err: err, revision: revision, client: client, viewer: viewer}
	}
}

func (m Model) nextTick() tea.Cmd {
	if m.interval <= 0 {
		return nil
	}
	return tea.Tick(m.interval, func(time.Time) tea.Msg { return tick{} })
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case spinnerTick:
		if !m.loading {
			m.spinning = false
			return m, nil
		}
		m.spinnerFrame = (m.spinnerFrame + 1) % len(spinnerFrames)
		return m, nextSpinnerTick()
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case loaded:
		if msg.client != nil && msg.viewer != "" {
			m.client, m.authenticated = msg.client, true
			if msg.viewer != m.viewer {
				m.viewer = msg.viewer
				m.restoreCache()
			}
		}
		if msg.revision != m.revision {
			// A triage action happened during the refresh: fetch with fresh state.
			return m, m.load()
		}
		m.loading = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil // Preserve the previous inbox on network errors.
		}
		m.allItems, m.warnings = msg.result.Items, msg.result.Warnings
		m.updateVisible()
		m.err = nil
		m.updated = time.Now()
		m.cached = false
		if err := m.store.SaveInbox(m.viewer, m.snapshot()); err != nil {
			m.err = fmt.Errorf("save inbox cache: %w", err)
		}
	case opened:
		m.opening = false
		if msg.err != nil {
			m.err = fmt.Errorf("open browser: %w", msg.err)
		} else {
			viewer := msg.viewer
			if viewer == "" {
				viewer = m.viewer
			}
			m.dismissFor(msg.item, state.Opened, viewer)
		}
	case tick:
		cmd := m.nextTick()
		if !m.loading {
			spin := m.startLoading()
			return m, tea.Batch(cmd, spin, m.load())
		}
		return m, cmd
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "j", "down":
			m.cursor = min(m.cursor+1, max(0, len(m.items)-1))
		case "k", "up":
			m.cursor = max(0, m.cursor-1)
		case "g", "home":
			m.cursor = 0
		case "G", "end":
			m.cursor = max(0, len(m.items)-1)
		case "ctrl+d", "pgdown":
			m.cursor = min(m.cursor+m.rows()/2, max(0, len(m.items)-1))
		case "ctrl+u", "pgup":
			m.cursor = max(0, m.cursor-m.rows()/2)
		case "r":
			if !m.loading {
				spin := m.startLoading()
				return m, tea.Batch(spin, m.load())
			}
		case "d":
			m.showDrafts = !m.showDrafts
			m.updateVisible()
		case "u":
			if !m.opening {
				m.undo()
			}
		case "e", "m", "o", "enter":
			if len(m.items) == 0 || m.opening {
				break
			}
			item := m.items[m.cursor]
			switch msg.String() {
			case "e":
				m.dismiss(item, state.Archived)
			case "m":
				m.dismiss(item, state.Muted)
			default:
				m.opening = true
				m.status = "Opening " + item.Key()
				return m, func() tea.Msg {
					ctx, cancel := context.WithTimeout(m.ctx, 15*time.Second)
					defer cancel()
					return opened{item: item, err: m.open(ctx, item.URL), viewer: m.viewer}
				}
			}
		}
	}
	return m, nil
}

func (m *Model) dismiss(item github.Item, action state.Action) {
	m.dismissFor(item, action, m.viewer)
}

func (m *Model) dismissFor(item github.Item, action state.Action, viewer string) {
	if err := m.store.SetItem(viewer, toCache(item), state.Entry{Action: action, UpdatedAt: item.UpdatedAt}); err != nil {
		m.err = fmt.Errorf("save state (item kept): %w", err)
		return
	}
	if viewer != m.viewer {
		m.status = string(action) + " " + item.Key() + " (@" + viewer + ")"
		m.err = nil
		return
	}
	m.revision++
	m.stepBuddy()
	for i, candidate := range m.allItems {
		if candidate.Key() == item.Key() {
			m.allItems = append(m.allItems[:i], m.allItems[i+1:]...)
			break
		}
	}
	m.updateVisible()
	m.err = nil
	m.status = string(action) + " " + item.Key()
}

func (m *Model) undo() {
	event, ok, err := m.store.Undo(m.viewer)
	if err != nil {
		m.err = fmt.Errorf("undo state (action kept): %w", err)
		return
	}
	m.err = nil
	if !ok {
		m.status = "Nothing to undo"
		return
	}
	m.revision++ // Reject refreshes filtered using the decision we just reversed.
	m.stepBuddy()
	if event.RestoresItem() {
		found := false
		for _, item := range m.allItems {
			if item.StateKey(m.viewer) == event.Key {
				found = true
				break
			}
		}
		if !found {
			m.allItems = append(m.allItems, fromCache(*event.Item))
			sort.SliceStable(m.allItems, func(i, j int) bool {
				return m.allItems[i].UpdatedAt.After(m.allItems[j].UpdatedAt)
			})
		}
	}
	m.updateVisible()
	for i, item := range m.items {
		if item.StateKey(m.viewer) == event.Key {
			m.cursor = i
			break
		}
	}
	m.status = "Undid " + string(event.Entry.Action) + " " + event.Key
}

// Draft visibility is a local view toggle: no network request or triage action.
func (m *Model) updateVisible() {
	selected := ""
	if len(m.items) > 0 {
		selected = m.items[m.cursor].Key()
	}
	items := make([]github.Item, 0, len(m.allItems))
	for _, item := range m.allItems {
		if item.State == "open" && (m.showDrafts || !item.Draft) {
			items = append(items, item)
		}
	}
	m.items = items
	m.cursor = min(m.cursor, max(0, len(m.items)-1))
	for i, item := range m.items {
		if item.Key() == selected {
			m.cursor = i
			break
		}
	}
}

func (m Model) rows() int { return max(1, m.height-8) }

var (
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("75"))
	selectedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230")).Background(lipgloss.Color("62"))
	dimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	errorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
)

// clean strips terminal control characters from untrusted API content.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		return r
	}, s)
}

func fit(s string, width int) string {
	return lipgloss.NewStyle().MaxWidth(max(1, width)).Render(clean(s))
}

func formatAge(age time.Duration) string {
	if age < time.Minute {
		return "now"
	}
	if age > 48*time.Hour {
		return fmt.Sprintf("%dd", age/(24*time.Hour))
	}
	text := strings.TrimSuffix(age.Truncate(time.Minute).String(), "0s")
	if strings.HasSuffix(text, "h0m") {
		text = strings.TrimSuffix(text, "0m")
	}
	return text
}

func (m Model) View() string {
	var b strings.Builder
	now := time.Now()
	status := fmt.Sprintf("%d items · @%s", len(m.items), m.viewer)
	if m.viewer == "" {
		status = fmt.Sprintf("%d items · signing in…", len(m.items))
	}
	if m.cached {
		status += " · cached"
	}
	if m.showDrafts {
		status += " · drafts shown"
	} else {
		status += " · drafts hidden"
	}
	if m.loading {
		status += " · " + spinnerFrames[m.spinnerFrame] + " refreshing"
	} else if !m.updated.IsZero() {
		status += " · " + m.updated.Format("15:04")
	}
	b.WriteString(titleStyle.Render("lilbuddy") + "  " + dimStyle.Render(fit(status, m.width-10)) + "\n\n")
	rows := m.rows()
	start := max(0, m.cursor-rows/2)
	start = min(start, max(0, len(m.items)-rows))
	if len(m.items) == 0 {
		text := "Nothing needs your attention."
		if m.loading && !m.cached {
			text = "Finding your issues and pull requests…"
		} else if m.err != nil && m.updated.IsZero() {
			text = "Could not load your inbox. Press r to retry."
		}
		b.WriteString(fit(text, m.width) + "\n")
	}
	for i := start; i < min(start+rows, len(m.items)); i++ {
		item := m.items[i]
		prefix := "  "
		if i == m.cursor {
			prefix = "> "
		}
		label := item.State
		if item.Draft {
			label = "draft"
		}
		line := fit(fmt.Sprintf("%s%s %-5s %s  %s  [%s]", prefix, formatAge(now.Sub(item.UpdatedAt)), item.Kind, item.Key(), item.Title, label), m.width)
		if i == m.cursor {
			line = selectedStyle.Render(line)
		}
		b.WriteString(line + "\n")
	}
	b.WriteString(m.inboxPadding(rows))
	b.WriteByte('\n')
	if len(m.items) > 0 {
		item := m.items[m.cursor]
		b.WriteString(dimStyle.Render(fit(fmt.Sprintf("%d/%d · %s · updated %s", m.cursor+1, len(m.items), strings.Join(item.Reasons, ", "), item.UpdatedAt.Local().Format("Jan 02 15:04")), m.width)))
	}
	b.WriteByte('\n')
	if m.err != nil {
		b.WriteString(errorStyle.Render(fit(m.err.Error(), m.width)))
	} else if len(m.warnings) > 0 {
		b.WriteString(dimStyle.Render(fit(fmt.Sprintf("Warning: %s (%d total)", m.warnings[0], len(m.warnings)), m.width)))
	} else {
		b.WriteString(dimStyle.Render(fit(m.status, m.width)))
	}
	b.WriteByte('\n')
	b.WriteString(dimStyle.Render(fit("j/k ↑/↓ move · e archive · o open · m mute · u undo · d drafts · r refresh · q quit", m.width)))
	return b.String()
}

func OpenBrowser(ctx context.Context, target string) error {
	// Only GitHub HTTPS URLs are expected; prevent a bad API URL from becoming
	// a local file or a browser's privileged URL scheme.
	if !strings.HasPrefix(target, "https://github.com/") {
		return fmt.Errorf("refusing non-GitHub URL %q", target)
	}
	var command string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		command, args = "open", []string{target}
	case "windows":
		command, args = "rundll32", []string{"url.dll,FileProtocolHandler", target}
	default:
		command, args = "xdg-open", []string{target}
	}
	output, err := exec.CommandContext(ctx, command, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w %s", command, err, strings.TrimSpace(string(output)))
	}
	return nil
}
