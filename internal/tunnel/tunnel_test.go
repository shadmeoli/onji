package tunnel

import (
	"context"
	"errors"
	"strings"
	"testing"

	"onji/internal/discovery"
)

type transport struct {
	calls    []string
	removed  string
	failures int
}

func (t *transport) Forward(_ context.Context, s string) error {
	t.calls = append(t.calls, s)
	if len(t.calls) <= t.failures {
		return errors.New("address in use")
	}
	return nil
}
func (t *transport) CancelForward(_ context.Context, s string) error { t.removed = s; return nil }
func TestCollisionFallbackAndExactCancellation(t *testing.T) {
	tr := &transport{failures: 1}
	m := New(tr)
	l := discovery.Listener{Address: "::", Port: 3000}
	f, err := m.Add(context.Background(), l)
	if err != nil {
		t.Fatal(err)
	}
	if f.Local == "127.0.0.1:3000" || !strings.HasSuffix(f.Spec, ":[::1]:3000") {
		t.Fatalf("wrong forward: %+v", f)
	}
	if _, err := m.Add(context.Background(), l); err != nil {
		t.Fatal(err)
	}
	if len(tr.calls) != 2 {
		t.Fatal("duplicate forward created")
	}
	if err := m.Remove(context.Background(), l.ID()); err != nil {
		t.Fatal(err)
	}
	if tr.removed != f.Spec {
		t.Fatal("cancelled a different forward")
	}
}
func TestRetriesAreBounded(t *testing.T) {
	tr := &transport{failures: 100}
	m := New(tr)
	if _, err := m.Add(context.Background(), discovery.Listener{Address: "127.0.0.1", Port: 3000}); err == nil {
		t.Fatal("expected failure")
	}
	if len(tr.calls) != 8 || len(m.active) != 0 {
		t.Fatal("unbounded retries or phantom tunnel")
	}
}
