// Package ssh owns OpenSSH child processes. Forwarded traffic never enters Go.
package ssh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Runner interface {
	Run(context.Context, string) ([]byte, error)
}

// cappedBuffer keeps draining output after reaching its limit, so a verbose
// child cannot block on a full pipe or grow the daemon's heap indefinitely.
type cappedBuffer struct {
	mu       sync.Mutex
	b        bytes.Buffer
	limit    int
	overflow bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	left := b.limit - b.b.Len()
	if len(p) > left {
		p = p[:left]
		b.overflow = true
	}
	_, _ = b.b.Write(p)
	return n, nil
}

func (b *cappedBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }

func command(ctx context.Context, args ...string) *exec.Cmd {
	c := exec.CommandContext(ctx, "ssh", args...)
	// Include ProxyJump/ProxyCommand descendants in cancellation. Pdeathsig
	// ensures that a daemon crash does not leave its SSH master behind.
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	c.Cancel = func() error {
		err := syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	c.WaitDelay = time.Second
	return c
}

func run(ctx context.Context, args ...string) ([]byte, error) {
	out, stderr := &cappedBuffer{limit: 2 << 20}, &cappedBuffer{limit: 4096}
	c := command(ctx, args...)
	c.Stdout, c.Stderr = out, stderr
	err := c.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, fmt.Errorf("ssh: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if out.overflow {
		return nil, errors.New("SSH output exceeded 2 MiB; narrow discovery rules")
	}
	return []byte(out.String()), nil
}

type Master struct {
	target, path string
	cancel       context.CancelFunc
	done         chan struct{}
	cmd          *exec.Cmd
	stderr       *cappedBuffer
	err          error // published by closing done
}

func Connect(lifetime, attempt context.Context, target, path string) (*Master, error) {
	ctx, cancel := context.WithCancel(lifetime)
	m := &Master{target: target, path: path, cancel: cancel, done: make(chan struct{}), stderr: &cappedBuffer{limit: 4096}}
	args := []string{"-M", "-N", "-T", "-S", path,
		"-o", "ControlMaster=yes", "-o", "ControlPersist=no", "-o", "ForkAfterAuthentication=no",
		"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ClearAllForwardings=yes",
		"-o", "ExitOnForwardFailure=yes", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3",
		"-o", "ConnectTimeout=10", "-o", "ConnectionAttempts=1", "--", target}
	m.cmd = command(ctx, args...)
	m.cmd.Stderr = m.stderr
	if err := m.cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	go func() { m.err = m.cmd.Wait(); close(m.done) }()
	timer := time.NewTicker(100 * time.Millisecond)
	defer timer.Stop()
	for {
		select {
		case <-attempt.Done():
			m.Close()
			return nil, attempt.Err()
		case <-m.done:
			m.Close()
			return nil, fmt.Errorf("SSH master exited: %v: %s", m.err, m.stderr.String())
		case <-timer.C:
			if _, err := os.Stat(path); err != nil {
				continue
			}
			if _, err := run(attempt, m.control("check")...); err == nil {
				return m, nil
			}
		}
	}
}

func (m *Master) control(op string) []string {
	return []string{"-S", m.path, "-O", op, "-o", "BatchMode=yes", "--", m.target}
}
func (m *Master) Alive() bool {
	select {
	case <-m.done:
		return false
	default:
		return true
	}
}
func (m *Master) Close() error {
	m.cancel()
	<-m.done
	_ = os.Remove(m.path)
	return nil
}

func (m *Master) Run(ctx context.Context, script string) ([]byte, error) {
	if !m.Alive() {
		return nil, errors.New("SSH master is disconnected")
	}
	// ProxyCommand=false prevents a new unaffiliated SSH connection if the
	// control socket vanishes between the liveness check and exec.
	return run(ctx, "-T", "-S", m.path, "-o", "ControlMaster=no", "-o", "ProxyCommand=false",
		"-o", "BatchMode=yes", "-o", "ClearAllForwardings=yes", "-o", "RemoteCommand=none", "--", m.target, script)
}

func (m *Master) forwarding(ctx context.Context, op, spec string) error {
	_, err := run(ctx, "-S", m.path, "-O", op, "-o", "BatchMode=yes", "-o", "ExitOnForwardFailure=yes", "-L", spec, "--", m.target)
	return err
}
func (m *Master) Forward(ctx context.Context, spec string) error {
	return m.forwarding(ctx, "forward", spec)
}
func (m *Master) CancelForward(ctx context.Context, spec string) error {
	return m.forwarding(ctx, "cancel", spec)
}

func Quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func Python(ctx context.Context, r Runner, source string, args ...string) ([]byte, error) {
	// Isolated mode excludes the remote working directory and PYTHONPATH from
	// imports, so an application's signal.py cannot shadow the standard library.
	cmd := "python3 -I -c " + Quote(source)
	for _, arg := range args {
		cmd += " " + Quote(arg)
	}
	return r.Run(ctx, cmd)
}
