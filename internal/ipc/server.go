// Package ipc exposes a versioned HTTP API over a private Unix domain socket.
package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"onji/internal/daemon"
	"onji/internal/process"
)

type Backend interface {
	Snapshot() daemon.State
	SetForward(context.Context, string, string, bool) error
	Processes(context.Context, string) ([]process.Info, error)
	Signal(context.Context, string, int, string, string, bool) error
}
type ForwardRequest struct {
	Host      string `json:"host"`
	ServiceID string `json:"service_id"`
	Enabled   bool   `json:"enabled"`
}
type SignalRequest struct {
	Host      string `json:"host"`
	PID       int    `json:"pid"`
	Identity  string `json:"identity"`
	Signal    string `json:"signal"`
	Confirmed bool   `json:"confirmed"`
}

// Endpoint holds the instance lock for its entire lifetime. Never unlink the
// lock file: another process could otherwise acquire a different inode.
type Endpoint struct {
	Listener net.Listener
	lock     *os.File
}

func Listen(path string) (*Endpoint, error) {
	if !filepath.IsAbs(path) || len(path) > 100 {
		return nil, errors.New("socket must be an absolute path of at most 100 bytes")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm()&0077 != 0 || stat.Uid != uint32(os.Getuid()) {
		return nil, errors.New("socket directory must be owned by you with permissions 0700")
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("another onjid instance is using this socket")
	}
	failed := true
	defer func() {
		if failed {
			lock.Close()
		}
	}()
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("refusing to replace a non-socket file")
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		ln.Close()
		return nil, err
	}
	failed = false
	return &Endpoint{Listener: ln, lock: lock}, nil
}
func (e *Endpoint) Close() error {
	err := e.Listener.Close()
	e.lock.Close()
	return err
}

func Handler(b Backend) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/state", func(w http.ResponseWriter, r *http.Request) { respond(w, b.Snapshot(), nil) })
	mux.HandleFunc("GET /v1/processes", func(w http.ResponseWriter, r *http.Request) {
		p, err := b.Processes(r.Context(), r.URL.Query().Get("host"))
		respond(w, p, err)
	})
	mux.HandleFunc("POST /v1/forward", func(w http.ResponseWriter, r *http.Request) {
		var q ForwardRequest
		if !decode(w, r, &q) {
			return
		}
		respond(w, struct{}{}, b.SetForward(r.Context(), q.Host, q.ServiceID, q.Enabled))
	})
	mux.HandleFunc("POST /v1/signal", func(w http.ResponseWriter, r *http.Request) {
		var q SignalRequest
		if !decode(w, r, &q) {
			return
		}
		respond(w, struct{}{}, b.Signal(r.Context(), q.Host, q.PID, q.Identity, q.Signal, q.Confirmed))
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if r.Header.Get("Origin") != "" {
			respond(w, nil, errors.New("browser requests are not supported"))
			return
		}
		mux.ServeHTTP(w, r)
	})
}
func decode(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		respond(w, nil, err)
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		respond(w, nil, errors.New("expected one JSON object"))
		return false
	}
	return true
}
func respond(w http.ResponseWriter, value any, err error) {
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		value = map[string]string{"error": err.Error()}
	}
	_ = json.NewEncoder(w).Encode(value)
}

func Serve(ctx context.Context, endpoint *Endpoint, backend Backend, timeout time.Duration) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ln := &limitedListener{Listener: endpoint.Listener, slots: make(chan struct{}, 32), done: make(chan struct{})}
	server := &http.Server{Handler: Handler(backend), ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second,
		WriteTimeout: timeout + 5*time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 4096,
		BaseContext: func(net.Listener) context.Context { return ctx }}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
		}
	}()
	err := server.Serve(ln)
	if ctx.Err() == nil {
		cancel()
		_ = server.Close()
		<-finished
		return fmt.Errorf("IPC server: %w", err)
	}
	<-finished
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

type limitedListener struct {
	net.Listener
	slots chan struct{}
	done  chan struct{}
	once  sync.Once
}

func (l *limitedListener) Accept() (net.Conn, error) {
	select {
	case l.slots <- struct{}{}:
	case <-l.done:
		return nil, net.ErrClosed
	}
	c, err := l.Listener.Accept()
	if err != nil {
		<-l.slots
		return nil, err
	}
	return &limitedConn{Conn: c, release: func() { <-l.slots }}, nil
}
func (l *limitedListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return l.Listener.Close()
}

type limitedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *limitedConn) Close() error { err := c.Conn.Close(); c.once.Do(c.release); return err }
