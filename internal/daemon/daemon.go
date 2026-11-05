// Package daemon supervises one serial worker per host. Workers own their
// connections and tunnels; readers only receive copies of published state.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"runtime"
	"runtime/metrics"
	"sort"
	"sync"
	"time"

	"onji/internal/config"
	"onji/internal/discovery"
	"onji/internal/process"
	"onji/internal/ssh"
	"onji/internal/tunnel"
)

type Service struct {
	discovery.Listener
	ID      string `json:"id"`
	Local   string `json:"local"`
	Enabled bool   `json:"enabled"`
	Status  string `json:"status"`
}
type HostState struct {
	Name     string    `json:"name"`
	SSH      string    `json:"ssh"`
	Status   string    `json:"status"`
	Error    string    `json:"error,omitempty"`
	LastScan time.Time `json:"last_scan"`
	Services []Service `json:"services"`
}
type Stats struct {
	Goroutines     int     `json:"goroutines"`
	HeapBytes      uint64  `json:"heap_bytes"`
	AllocatedBytes uint64  `json:"allocated_bytes"`
	GCCPUSeconds   float64 `json:"gc_cpu_seconds"`
}
type State struct {
	Started time.Time   `json:"started"`
	Hosts   []HostState `json:"hosts"`
	Stats   Stats       `json:"stats"`
}

type connection interface {
	ssh.Runner
	tunnel.Transport
	Alive() bool
	Close() error
}
type connector func(context.Context, context.Context, string, string) (connection, error)
type reply struct {
	processes []process.Info
	err       error
}
type request struct {
	ctx                        context.Context
	kind, id, identity, signal string
	enabled                    bool
	pid                        int
	reply                      chan reply
}

type Daemon struct {
	ctx     context.Context
	started time.Time
	workers map[string]*worker
	mu      sync.RWMutex
	states  map[string]HostState
	wg      sync.WaitGroup
}
type worker struct {
	d                 *Daemon
	cfg               config.Config
	host              config.Host
	path              string
	connect           connector
	commands          chan request
	conn              connection
	tunnels           *tunnel.Manager
	records           map[string]Service
	baseline          map[string]bool
	initialized       bool
	status, lastError string
	lastScan          time.Time
}

func Start(ctx context.Context, cfg config.Config, controlDir string) (*Daemon, error) {
	return start(ctx, cfg, controlDir, func(life, attempt context.Context, host, path string) (connection, error) {
		return ssh.Connect(life, attempt, host, path)
	})
}

func start(ctx context.Context, cfg config.Config, dir string, connect connector) (*Daemon, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	d := &Daemon{ctx: ctx, started: time.Now(), workers: make(map[string]*worker), states: make(map[string]HostState)}
	for i, host := range cfg.Hosts {
		path := filepath.Join(dir, fmt.Sprintf("h%d", i))
		if len(path) > 100 {
			return nil, errors.New("SSH control socket path is too long; use a shorter runtime directory")
		}
		w := &worker{d: d, cfg: cfg, host: host, path: path, connect: connect, commands: make(chan request, 8), records: make(map[string]Service), baseline: make(map[string]bool), status: "connecting"}
		d.workers[host.Name] = w
		w.publish()
	}
	for _, w := range d.workers {
		d.wg.Add(1)
		go func() { defer d.wg.Done(); w.run() }()
	}
	return d, nil
}

func (d *Daemon) Wait() { d.wg.Wait() }

func (d *Daemon) Snapshot() State {
	s := State{Started: d.started, Hosts: make([]HostState, 0, len(d.workers))}
	d.mu.RLock()
	for _, state := range d.states {
		state.Services = append([]Service{}, state.Services...)
		s.Hosts = append(s.Hosts, state)
	}
	d.mu.RUnlock()
	sort.Slice(s.Hosts, func(i, j int) bool { return s.Hosts[i].Name < s.Hosts[j].Name })
	samples := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}, {Name: "/gc/heap/allocs:bytes"}, {Name: "/cpu/classes/gc/total:cpu-seconds"}}
	metrics.Read(samples)
	s.Stats = Stats{Goroutines: runtime.NumGoroutine(), HeapBytes: samples[0].Value.Uint64(), AllocatedBytes: samples[1].Value.Uint64(), GCCPUSeconds: samples[2].Value.Float64()}
	return s
}

func (d *Daemon) call(ctx context.Context, host string, r request) (reply, error) {
	w, ok := d.workers[host]
	if !ok {
		return reply{}, errors.New("unknown host")
	}
	r.ctx, r.reply = ctx, make(chan reply, 1)
	if err := ctx.Err(); err != nil {
		return reply{}, err
	}
	select {
	case <-d.ctx.Done():
		return reply{}, errors.New("daemon is stopping")
	case w.commands <- r:
	default:
		return reply{}, errors.New("host is busy; retry shortly")
	}
	select {
	case <-ctx.Done():
		return reply{}, ctx.Err()
	case <-d.ctx.Done():
		return reply{}, errors.New("daemon is stopping")
	case result := <-r.reply:
		return result, result.err
	}
}

func (d *Daemon) SetForward(ctx context.Context, host, id string, enabled bool) error {
	_, err := d.call(ctx, host, request{kind: "forward", id: id, enabled: enabled})
	return err
}
func (d *Daemon) Processes(ctx context.Context, host string) ([]process.Info, error) {
	r, err := d.call(ctx, host, request{kind: "processes"})
	return r.processes, err
}
func (d *Daemon) Signal(ctx context.Context, host string, pid int, identity, signal string, confirmed bool) error {
	if !confirmed {
		return errors.New("process termination requires explicit confirmation")
	}
	_, err := d.call(ctx, host, request{kind: "signal", pid: pid, identity: identity, signal: signal})
	return err
}

func (w *worker) run() {
	timer := time.NewTimer(0)
	defer timer.Stop()
	defer w.disconnect()
	backoff := time.Second
	for {
		select {
		case <-w.d.ctx.Done():
			return
		case r := <-w.commands:
			if r.ctx.Err() != nil {
				r.reply <- reply{err: r.ctx.Err()}
				continue
			}
			r.reply <- w.handle(r)
		case <-timer.C:
			err := w.poll()
			if err != nil {
				w.failed(err)
				timer.Reset(backoff + time.Duration(rand.IntN(250))*time.Millisecond)
				backoff = min(backoff*2, 30*time.Second)
			} else {
				backoff = time.Second
				timer.Reset(time.Duration(w.cfg.PollInterval))
			}
		}
	}
}

func (w *worker) poll() error {
	ctx, cancel := context.WithTimeout(w.d.ctx, time.Duration(w.cfg.CommandTimeout))
	defer cancel()
	if w.conn != nil && !w.conn.Alive() {
		w.disconnect()
	}
	if w.conn == nil {
		conn, err := w.connect(w.d.ctx, ctx, w.host.SSH, w.path)
		if err != nil {
			return err
		}
		w.conn, w.tunnels = conn, tunnel.New(conn)
	}
	listeners, err := discovery.Scan(ctx, w.conn, w.host, w.cfg.MaxServices)
	if err != nil {
		return err
	}
	if !w.initialized {
		if w.host.Discovery == "new" {
			for _, l := range listeners {
				w.baseline[l.ID()] = true
			}
		}
		w.initialized = true
	}
	next := make(map[string]Service, len(listeners))
	for _, l := range listeners {
		enabled := w.host.Automatic() && !w.baseline[l.ID()]
		if old, ok := w.records[l.ID()]; ok {
			enabled = old.Enabled
		}
		next[l.ID()] = Service{Listener: l, ID: l.ID(), Enabled: enabled, Status: "disabled"}
	}
	for id := range w.records {
		if _, exists := next[id]; !exists {
			if err := w.tunnels.Remove(ctx, id); err != nil {
				return err
			}
		}
	}
	w.records = next
	for id, s := range w.records {
		if s.Enabled {
			f, err := w.tunnels.Add(ctx, s.Listener)
			if err != nil {
				return err
			}
			s.Local, s.Status = f.Local, "forwarded"
		}
		w.records[id] = s
	}
	w.status, w.lastError, w.lastScan = "connected", "", time.Now()
	w.publish()
	return nil
}

func (w *worker) handle(r request) reply {
	ctx, cancel := context.WithTimeout(r.ctx, time.Duration(w.cfg.CommandTimeout))
	stop := context.AfterFunc(w.d.ctx, cancel)
	defer func() { stop(); cancel() }()
	if r.kind == "forward" {
		s, ok := w.records[r.id]
		if !ok {
			return reply{err: errors.New("service no longer exists; refresh the list")}
		}
		s.Enabled, s.Local, s.Status = r.enabled, "", "disabled"
		if s.Enabled {
			s.Status = "disconnected"
		}
		w.records[r.id] = s
		if w.conn != nil {
			var err error
			if s.Enabled {
				var f tunnel.Forward
				f, err = w.tunnels.Add(ctx, s.Listener)
				if err == nil {
					s.Local, s.Status = f.Local, "forwarded"
				}
			} else {
				err = w.tunnels.Remove(ctx, s.ID)
			}
			if err != nil {
				w.failed(err)
				return reply{err: err}
			}
			w.records[r.id] = s
		}
		w.publish()
		return reply{}
	}
	if w.conn == nil || !w.conn.Alive() {
		return reply{err: errors.New("host is disconnected")}
	}
	switch r.kind {
	case "processes":
		p, err := process.List(ctx, w.conn, w.cfg.MaxProcesses)
		return reply{processes: p, err: err}
	case "signal":
		return reply{err: process.Signal(ctx, w.conn, r.pid, r.identity, r.signal)}
	default:
		return reply{err: errors.New("unknown command")}
	}
}

func (w *worker) disconnect() {
	if w.conn != nil {
		_ = w.conn.Close()
	}
	w.conn, w.tunnels = nil, nil
	for id, s := range w.records {
		s.Local = ""
		if s.Enabled {
			s.Status = "disconnected"
		} else {
			s.Status = "disabled"
		}
		w.records[id] = s
	}
}
func (w *worker) failed(err error) {
	w.disconnect()
	w.status, w.lastError = "retrying", err.Error()
	if len(w.lastError) > 1024 {
		w.lastError = w.lastError[:1024]
	}
	w.publish()
}
func (w *worker) publish() {
	h := HostState{Name: w.host.Name, SSH: w.host.SSH, Status: w.status, Error: w.lastError, LastScan: w.lastScan, Services: make([]Service, 0, len(w.records))}
	for _, s := range w.records {
		h.Services = append(h.Services, s)
	}
	sort.Slice(h.Services, func(i, j int) bool { return h.Services[i].ID < h.Services[j].ID })
	w.d.mu.Lock()
	w.d.states[h.Name] = h
	w.d.mu.Unlock()
}
