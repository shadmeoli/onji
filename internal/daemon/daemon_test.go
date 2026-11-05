package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"onji/internal/config"
	"onji/internal/discovery"
)

type fakeConn struct {
	mu              sync.Mutex
	listeners       []discovery.Listener
	active          map[string]bool
	alive           bool
	failScan        bool
	closed, signals int
}

func (f *fakeConn) Run(ctx context.Context, script string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.Contains(script, "pidfd_send_signal") {
		f.signals++
		return []byte("{}"), nil
	}
	if f.failScan {
		return nil, errors.New("network lost")
	}
	return json.Marshal(f.listeners)
}
func (f *fakeConn) Alive() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.alive }
func (f *fakeConn) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.alive = false
	f.closed++
	f.active = map[string]bool{}
	return nil
}
func (f *fakeConn) Forward(_ context.Context, s string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.active[s] = true
	return nil
}
func (f *fakeConn) CancelForward(_ context.Context, s string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.active, s)
	return nil
}

func testWorker(t *testing.T, mode string) (*worker, *fakeConn) {
	t.Helper()
	f := &fakeConn{alive: true, active: map[string]bool{}, listeners: []discovery.Listener{{Address: "127.0.0.1", Port: 3000, PID: 42, Identity: "boot:100"}}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cfg := config.Config{PollInterval: config.Duration(time.Second), CommandTimeout: config.Duration(time.Second), MaxServices: 10, MaxProcesses: 10, Hosts: []config.Host{{Name: "dev", SSH: "dev", Discovery: mode}}}
	d := &Daemon{ctx: ctx, states: map[string]HostState{}, workers: map[string]*worker{}, started: time.Now()}
	w := &worker{d: d, cfg: cfg, host: cfg.Hosts[0], records: map[string]Service{}, baseline: map[string]bool{}}
	w.connect = func(context.Context, context.Context, string, string) (connection, error) {
		f.alive = true
		return f, nil
	}
	t.Cleanup(w.disconnect)
	return w, f
}

func TestReconcileDisableReconnectAndRemove(t *testing.T) {
	w, f := testWorker(t, "all")
	if err := w.poll(); err != nil {
		t.Fatal(err)
	}
	id := "127.0.0.1:3000"
	if w.records[id].Local == "" || len(f.active) != 1 {
		t.Fatal("service not forwarded")
	}
	r := w.handle(request{ctx: context.Background(), kind: "forward", id: id, enabled: false})
	if r.err != nil || len(f.active) != 0 || f.signals != 0 {
		t.Fatal("disable did not only remove tunnel")
	}
	w.failed(errors.New("disconnected"))
	if err := w.poll(); err != nil {
		t.Fatal(err)
	}
	if w.records[id].Enabled || len(f.active) != 0 {
		t.Fatal("disabled service forwarded after reconnect")
	}
	r = w.handle(request{ctx: context.Background(), kind: "forward", id: id, enabled: true})
	if r.err != nil || len(f.active) != 1 {
		t.Fatal("could not re-enable tunnel")
	}
	f.listeners = nil
	if err := w.poll(); err != nil {
		t.Fatal(err)
	}
	if len(w.records) != 0 || len(f.active) != 0 {
		t.Fatal("disappeared service retained")
	}
}

func TestNewOnlyAndFailedScanDoesNotForgetServices(t *testing.T) {
	w, f := testWorker(t, "new")
	if err := w.poll(); err != nil {
		t.Fatal(err)
	}
	if len(f.active) != 0 {
		t.Fatal("baseline service forwarded")
	}
	f.listeners = append(f.listeners, discovery.Listener{Address: "127.0.0.1", Port: 4000})
	if err := w.poll(); err != nil {
		t.Fatal(err)
	}
	if len(f.active) != 1 {
		t.Fatal("new service not forwarded")
	}
	f.failScan = true
	err := w.poll()
	if err == nil {
		t.Fatal("expected scan failure")
	}
	w.failed(err)
	if len(w.records) != 2 || len(f.active) != 0 {
		t.Fatal("failure treated as empty discovery or tunnels leaked")
	}
	f.failScan = false
	if err := w.poll(); err != nil {
		t.Fatal(err)
	}
	if len(f.active) != 1 {
		t.Fatal("reconnect lost new-only baseline")
	}
}

func TestCancelledQueuedCommandAndShutdown(t *testing.T) {
	w, f := testWorker(t, "all")
	ctx, cancel := context.WithCancel(context.Background())
	d, err := start(ctx, w.cfg, "/tmp", w.connect)
	if err != nil {
		t.Fatal(err)
	}
	requestCtx, abort := context.WithCancel(ctx)
	abort()
	if err := d.SetForward(requestCtx, "dev", "127.0.0.1:3000", false); err == nil {
		t.Fatal("cancelled command accepted")
	}
	// Read state concurrently with workers while the race detector is active.
	for range 100 {
		_ = d.Snapshot()
	}
	cancel()
	done := make(chan struct{})
	go func() { d.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("workers did not stop")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.active) != 0 {
		t.Fatal("tunnels survived shutdown")
	}
}

// Run with ONJI_SOAK_DURATION=48h go test -run TestSoak -timeout 49h ./internal/daemon.
// This accelerates host churn using a fake transport; real SSH is covered by
// the opt-in integration suite. It does not claim to measure OpenSSH's memory.
func TestSoak(t *testing.T) {
	duration := os.Getenv("ONJI_SOAK_DURATION")
	if duration == "" {
		t.Skip("set ONJI_SOAK_DURATION to run the soak test")
	}
	d, err := time.ParseDuration(duration)
	if err != nil || d <= 0 {
		t.Fatal("invalid ONJI_SOAK_DURATION")
	}
	w, f := testWorker(t, "all")
	baselineG := runtime.NumGoroutine()
	var initial runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&initial)
	start := time.Now()
	nextReport := start.Add(time.Minute)
	cycles := 0
	for time.Since(start) < d {
		f.listeners = []discovery.Listener{{Address: "127.0.0.1", Port: 3000 + cycles%1000}}
		if err := w.poll(); err != nil {
			t.Fatal(err)
		}
		if len(w.records) > 1 || len(f.active) > 1 {
			t.Fatal("state grew with history")
		}
		w.failed(fmt.Errorf("simulated disconnect %d", cycles))
		if err := w.poll(); err != nil {
			t.Fatal(err)
		}
		f.listeners = nil
		if err := w.poll(); err != nil {
			t.Fatal(err)
		}
		if len(w.records) != 0 || len(f.active) != 0 {
			t.Fatal("resources not reconciled")
		}
		cycles++
		if time.Now().After(nextReport) {
			t.Logf("cycles=%d elapsed=%s", cycles, time.Since(start))
			nextReport = time.Now().Add(time.Minute)
		}
		time.Sleep(time.Millisecond)
	}
	runtime.GC()
	var final runtime.MemStats
	runtime.ReadMemStats(&final)
	if final.HeapAlloc > initial.HeapAlloc+8<<20 {
		t.Fatalf("retained heap grew: %d -> %d", initial.HeapAlloc, final.HeapAlloc)
	}
	if runtime.NumGoroutine() > baselineG+2 {
		t.Fatal("goroutine count grew")
	}
	t.Logf("%d churn cycles; heap %d -> %d bytes", cycles, initial.HeapAlloc, final.HeapAlloc)
}
