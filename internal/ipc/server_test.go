package ipc

import (
	"context"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"onji/internal/daemon"
	"onji/internal/process"
)

type backend struct{ signals int }

func (*backend) Snapshot() daemon.State {
	return daemon.State{Hosts: []daemon.HostState{{Name: "dev", Status: "connected"}}}
}
func (*backend) SetForward(context.Context, string, string, bool) error { return nil }
func (*backend) Processes(context.Context, string) ([]process.Info, error) {
	return []process.Info{}, nil
}
func (b *backend) Signal(_ context.Context, _ string, _ int, _ string, _ string, confirmed bool) error {
	if confirmed {
		b.signals++
	}
	return nil
}

func TestPrivateSocketLockAndClientLifecycle(t *testing.T) {
	dir, err := os.MkdirTemp("", "onji-ipc-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "onji.sock")
	ep, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ep.Close()
	if second, err := Listen(path); err == nil {
		second.Close()
		t.Fatal("second daemon acquired live socket")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal("socket is not private")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, ep, &backend{}, time.Second) }()
	for range 20 {
		c := NewClient(path)
		s, err := c.State(ctx)
		c.Close()
		if err != nil || len(s.Hosts) != 1 {
			t.Fatalf("state failed: %v", err)
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("IPC did not shut down")
	}
}

func TestRejectOversizedUnknownAndTrailingRequests(t *testing.T) {
	b := &backend{}
	handler := Handler(b)
	for _, body := range []string{`{"unknown":1}`, `{} {}`, `{"host":"` + strings.Repeat("x", 5000) + `"}`} {
		r := httptest.NewRequest("POST", "/v1/signal", strings.NewReader(body))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("accepted invalid request: %d", w.Code)
		}
	}
	if b.signals != 0 {
		t.Fatal("invalid input reached backend")
	}
}

func TestDoesNotReplaceOrdinaryFiles(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	path := filepath.Join(dir, "socket")
	if err := os.WriteFile(path, []byte("keep me"), 0600); err != nil {
		t.Fatal(err)
	}
	if ep, err := Listen(path); err == nil {
		ep.Close()
		t.Fatal("replaced ordinary file")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b, _ := io.ReadAll(f)
	if string(b) != "keep me" {
		t.Fatal("file changed")
	}
}
