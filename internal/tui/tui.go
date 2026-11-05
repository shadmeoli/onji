package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"charm.land/bubbles/v2/table"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"onji/internal/daemon"
	"onji/internal/ipc"
	"onji/internal/process"
)

var accent = lipgloss.NewStyle().Foreground(lipgloss.Color("#82D9C8")).Bold(true)
var muted = lipgloss.NewStyle().Foreground(lipgloss.Color("#8E98AB"))
var warning = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFBB78"))

type refreshMsg struct {
	state     daemon.State
	processes []process.Info
	host      string
	err       error
}
type tickMsg struct{}
type actionMsg struct{ err error }
type row struct {
	host    string
	service daemon.Service
	process process.Info
}

func (r row) key() string {
	return r.host + "/" + r.service.ID + "/" + r.process.Identity + "/" + strconv.Itoa(r.process.PID)
}

type model struct {
	ctx           context.Context
	client        *ipc.Client
	state         daemon.State
	table         table.Model
	filter        textinput.Model
	rows          []row
	processes     []process.Info
	processHost   string
	width, height int
	message       string
	busy          bool
	confirm       *ipc.SignalRequest
}

func Run(ctx context.Context, client *ipc.Client) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	input := textinput.New()
	input.Placeholder = "Filter services or processes"
	input.Prompt = "/ "
	input.CharLimit = 100
	input.SetVirtualCursor(true)
	m := model{ctx: ctx, client: client, filter: input, width: 110, height: 30, table: table.New(table.WithFocused(true))}
	m.resize()
	_, err := tea.NewProgram(m, tea.WithContext(ctx)).Run()
	return err
}

func (m model) Init() tea.Cmd { return m.refresh() }
func (m model) refresh() tea.Cmd {
	client, ctx, host := m.client, m.ctx, m.processHost
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		s, err := client.State(ctx)
		var p []process.Info
		if err == nil && host != "" {
			p, err = client.Processes(ctx, host)
		}
		return refreshMsg{state: s, processes: p, host: host, err: err}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resize()
		m.rebuild()
		return m, nil
	case refreshMsg:
		if msg.err != nil {
			m.message = msg.err.Error()
		} else {
			m.state = msg.state
			if msg.host == m.processHost {
				m.processes = msg.processes
			}
			if strings.HasPrefix(m.message, "cannot reach") {
				m.message = ""
			}
		}
		m.rebuild()
		return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg { return tickMsg{} })
	case tickMsg:
		return m, m.refresh()
	case actionMsg:
		m.busy = false
		if msg.err != nil {
			m.message = msg.err.Error()
		} else {
			m.message = "Request completed. Status refreshes automatically."
		}
		return m, nil
	case tea.KeyPressMsg:
		key := msg.String()
		if key == "ctrl+c" {
			return m, tea.Quit
		}
		if m.filter.Focused() {
			if key == "esc" || key == "enter" {
				m.filter.Blur()
				m.table.Focus()
				return m, nil
			}
			var cmd tea.Cmd
			m.filter, cmd = m.filter.Update(msg)
			m.rebuild()
			return m, cmd
		}
		if key == "q" {
			return m, tea.Quit
		}
		if m.confirm != nil {
			if key == "y" {
				q := *m.confirm
				q.Confirmed = true
				m.confirm = nil
				m.busy = true
				return m, func() tea.Msg { return actionMsg{m.client.Signal(m.ctx, q)} }
			}
			if key == "n" || key == "esc" {
				m.confirm = nil
			}
			return m, nil
		}
		switch key {
		case "/":
			m.table.Blur()
			return m, m.filter.Focus()
		case "esc":
			m.filter.Reset()
			m.rebuild()
			return m, nil
		case "p", "tab":
			if m.processHost != "" {
				m.processHost = ""
			} else if r, ok := m.selected(); ok {
				m.processHost = r.host
			} else if len(m.state.Hosts) > 0 {
				m.processHost = m.state.Hosts[0].Name
			}
			m.processes = nil
			m.filter.Reset()
			m.resize()
			m.rebuild()
			return m, nil
		case "[", "]":
			if m.processHost != "" && len(m.state.Hosts) > 0 {
				for i, h := range m.state.Hosts {
					if h.Name == m.processHost {
						delta := 1
						if key == "[" {
							delta = -1
						}
						m.processHost = m.state.Hosts[(i+delta+len(m.state.Hosts))%len(m.state.Hosts)].Name
						break
					}
				}
				m.processes = nil
				m.rebuild()
			}
			return m, nil
		case "f":
			if m.busy || m.processHost != "" {
				return m, nil
			}
			if r, ok := m.selected(); ok {
				m.busy = true
				q := ipc.ForwardRequest{Host: r.host, ServiceID: r.service.ID, Enabled: !r.service.Enabled}
				return m, func() tea.Msg { return actionMsg{m.client.Forward(m.ctx, q)} }
			}
			return m, nil
		case "x", "X":
			if m.busy {
				return m, nil
			}
			if r, ok := m.selected(); ok {
				p := r.process
				if m.processHost == "" {
					p = process.Info{PID: r.service.PID, Identity: r.service.Identity}
				}
				if p.PID <= 1 || p.Identity == "" {
					m.message = "No process identity is available for this service."
					return m, nil
				}
				action := "TERM"
				if key == "X" {
					action = "KILL"
				}
				m.confirm = &ipc.SignalRequest{Host: r.host, PID: p.PID, Identity: p.Identity, Signal: action}
			}
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

func (m *model) resize() {
	m.table.SetHeight(max(3, m.height-13))
	m.table.SetWidth(max(20, m.width-4))
	m.filter.SetWidth(max(10, m.width-8))
	if m.processHost != "" {
		m.table.SetColumns([]table.Column{{Title: "PID", Width: 8}, {Title: "PROCESS", Width: max(16, m.width-36)}, {Title: "UID", Width: 8}})
	} else if m.width < 100 {
		w := max(10, (m.width-25)/3)
		m.table.SetColumns([]table.Column{{Title: "PROCESS", Width: w}, {Title: "REMOTE", Width: w}, {Title: "LOCAL", Width: w}, {Title: "STATUS", Width: 12}})
	} else {
		w := max(18, (m.width-63)/2)
		m.table.SetColumns([]table.Column{{Title: "HOST", Width: 12}, {Title: "PROCESS", Width: 16}, {Title: "PID", Width: 7}, {Title: "REMOTE", Width: w}, {Title: "LOCAL", Width: w}, {Title: "STATUS", Width: 12}})
	}
}

func (m model) selected() (row, bool) {
	i := m.table.Cursor()
	if i < 0 || i >= len(m.rows) {
		return row{}, false
	}
	return m.rows[i], true
}

func (m *model) rebuild() {
	selected, _ := m.selected()
	query := strings.ToLower(m.filter.Value())
	rows := []row{}
	if m.processHost != "" {
		for _, p := range m.processes {
			if strings.Contains(strings.ToLower(p.Name+" "+strconv.Itoa(p.PID)), query) {
				rows = append(rows, row{host: m.processHost, process: p})
			}
		}
	} else {
		for _, h := range m.state.Hosts {
			for _, s := range h.Services {
				if strings.Contains(strings.ToLower(h.Name+" "+s.Name+" "+s.ID+" "+s.Local+" "+strconv.Itoa(s.PID)), query) {
					rows = append(rows, row{host: h.Name, service: s})
				}
			}
		}
	}
	m.rows = rows
	values := make([]table.Row, 0, len(rows))
	cursor := 0
	for i, r := range rows {
		if r.key() == selected.key() {
			cursor = i
		}
		if m.processHost != "" {
			values = append(values, table.Row{strconv.Itoa(r.process.PID), clean(r.process.Name), strconv.Itoa(r.process.UID)})
			continue
		}
		s := r.service
		name := clean(s.Name)
		if name == "" {
			name = "unknown"
		}
		local := s.Local
		if local == "" {
			local = "—"
		}
		if m.width < 100 {
			values = append(values, table.Row{name, clean(s.ID), local, s.Status})
		} else {
			values = append(values, table.Row{clean(r.host), name, strconv.Itoa(s.PID), clean(s.ID), local, s.Status})
		}
	}
	m.table.SetRows(values)
	m.table.SetCursor(cursor)
}

func (m model) View() tea.View {
	if m.width < 60 || m.height < 18 {
		v := tea.NewView("Onji needs a terminal of at least 60 columns × 18 rows.\nResize the terminal or press q to detach.")
		v.AltScreen = true
		return v
	}
	title := "ONJI  /  Services"
	if m.processHost != "" {
		title = "ONJI  /  Processes on " + clean(m.processHost)
	}
	connected := 0
	for _, h := range m.state.Hosts {
		if h.Status == "connected" {
			connected++
		}
	}
	meta := fmt.Sprintf("%d/%d hosts connected · %d items · heap %.1f MiB", connected, len(m.state.Hosts), len(m.rows), float64(m.state.Stats.HeapBytes)/(1<<20))
	detail := "No matching items. Discovery runs in the background."
	if r, ok := m.selected(); ok {
		if m.processHost != "" {
			detail = fmt.Sprintf("%s · PID %d · %s", r.host, r.process.PID, r.process.Name)
		} else {
			detail = fmt.Sprintf("%s · PID %d · %s → %s", r.host, r.service.PID, r.service.ID, r.service.Local)
		}
	}
	notice := m.message
	if notice == "" {
		for _, h := range m.state.Hosts {
			if h.Error != "" {
				notice = h.Name + ": " + h.Error
				break
			}
		}
	}
	if m.busy {
		notice = "Applying request…"
	}
	if m.confirm != nil {
		notice = fmt.Sprintf("Send SIG%s to PID %d on %s? [y] confirm / [n] cancel", m.confirm.Signal, m.confirm.PID, m.confirm.Host)
	}
	help := "↑↓ navigate · / filter · f forwarding · p processes · q detach\nx stop · X force-kill (confirmation required)"
	if m.processHost != "" {
		help = "↑↓ navigate · / filter · [ ] host · p services · q detach\nx stop · X force-kill (confirmation required)"
	}
	content := strings.Join([]string{accent.Render(title), muted.Render(meta), "", m.filter.View(), m.table.View(), "", clean(detail), warning.Render(clean(notice)), muted.Render(help)}, "\n")
	v := tea.NewView(lipgloss.NewStyle().Padding(1, 2).MaxWidth(m.width).MaxHeight(m.height).Render(content))
	v.AltScreen = true
	return v
}

func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}
