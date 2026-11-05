package tui

import (
	"testing"

	"charm.land/bubbles/v2/table"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"onji/internal/daemon"
	"onji/internal/discovery"
)

func TestConfirmationKeepsOriginalIdentityAcrossRefresh(t *testing.T) {
	m := model{table: table.New(), filter: textinput.New(), width: 120, height: 30}
	m.resize()
	m.state = daemon.State{Hosts: []daemon.HostState{{Name: "dev", Services: []daemon.Service{{ID: "127.0.0.1:3000", Listener: discovery.Listener{PID: 123, Identity: "old", Name: "app"}}}}}}
	m.rebuild()
	next, _ := m.Update(tea.KeyPressMsg{Code: 'x'})
	m = next.(model)
	if m.confirm == nil || m.confirm.Identity != "old" {
		t.Fatal("missing confirmation")
	}
	m.state.Hosts[0].Services[0].Identity = "replacement"
	m.rebuild()
	if m.confirm.Identity != "old" {
		t.Fatal("refresh silently retargeted pending termination")
	}
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'n'})
	m = next.(model)
	if m.confirm != nil || cmd != nil {
		t.Fatal("cancel sent a command")
	}
}

func TestRemoteTextCannotInjectTerminalControls(t *testing.T) {
	if got := clean("app\x1b[2J\nname"); got != "app [2J name" {
		t.Fatalf("unsafe display text: %q", got)
	}
}
