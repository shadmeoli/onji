package tunnel

import (
	"context"
	"fmt"
	"net"
	"strconv"

	"onji/internal/discovery"
)

type Transport interface {
	Forward(context.Context, string) error
	CancelForward(context.Context, string) error
}
type Forward struct{ Local, Spec string }
type Manager struct {
	transport Transport
	active    map[string]Forward
}

func New(t Transport) *Manager                   { return &Manager{transport: t, active: make(map[string]Forward)} }
func (m *Manager) Get(id string) (Forward, bool) { f, ok := m.active[id]; return f, ok }

func (m *Manager) Add(ctx context.Context, l discovery.Listener) (Forward, error) {
	if f, ok := m.Get(l.ID()); ok {
		return f, nil
	}
	var last error
	for attempt := 0; attempt < 8; attempt++ {
		if err := ctx.Err(); err != nil {
			return Forward{}, err
		}
		port := l.Port
		if attempt > 0 {
			// OpenSSH owns the final bind. The temporary reservation is only a
			// candidate; a race is retried, never reported as a working tunnel.
			ln, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				return Forward{}, err
			}
			port = ln.Addr().(*net.TCPAddr).Port
			ln.Close()
		}
		local := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
		spec := local + ":" + net.JoinHostPort(l.Destination(), strconv.Itoa(l.Port))
		if err := m.transport.Forward(ctx, spec); err != nil {
			last = err
			continue
		}
		f := Forward{Local: local, Spec: spec}
		m.active[l.ID()] = f
		return f, nil
	}
	return Forward{}, fmt.Errorf("cannot create forward: %w", last)
}

func (m *Manager) Remove(ctx context.Context, id string) error {
	f, ok := m.active[id]
	if !ok {
		return nil
	}
	if err := m.transport.CancelForward(ctx, f.Spec); err != nil {
		return err
	}
	delete(m.active, id)
	return nil
}
