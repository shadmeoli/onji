package integration

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"onji/internal/config"
	"onji/internal/daemon"
	"onji/internal/ipc"
	"onji/internal/ssh"
)

// This fixture uses temporary keys and a private loopback sshd. It never
// changes ~/.ssh or connects to any configured external host.
func TestOpenSSHLifecycle(t *testing.T) {
	if os.Getenv("ONJI_INTEGRATION") != "1" {
		t.Skip("set ONJI_INTEGRATION=1; requires sshd, ssh-keygen, ss, and python3")
	}
	for _, bin := range []string{"sshd", "ssh-keygen", "ssh", "ss", "python3"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Fatal(err)
		}
	}
	dir, err := os.MkdirTemp("", "onji-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	write := func(name, body string, mode os.FileMode) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
		return path
	}
	for _, name := range []string{"host", "client"} {
		out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", filepath.Join(dir, name)).CombinedOutput()
		if err != nil {
			t.Fatalf("keygen: %v %s", err, out)
		}
	}
	public, err := os.ReadFile(filepath.Join(dir, "client.pub"))
	if err != nil {
		t.Fatal(err)
	}
	authorized := write("authorized_keys", string(public), 0600)
	port := freePort(t)
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	sshdConfig := write("sshd_config", fmt.Sprintf("ListenAddress 127.0.0.1\nPort %d\nHostKey %s/host\nAuthorizedKeysFile %s\nStrictModes no\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nUsePAM no\nAllowUsers %s\nPidFile %s/pid\nLogLevel ERROR\n", port, dir, authorized, u.Username, dir), 0600)
	sshdPath, _ := exec.LookPath("sshd")
	log, err := os.Create(filepath.Join(dir, "sshd.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	sshd := exec.Command(sshdPath, "-D", "-e", "-f", sshdConfig)
	sshd.Stdout, sshd.Stderr = log, log
	if err := sshd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		sshd.Process.Kill()
		sshd.Wait()
		if t.Failed() {
			b, _ := os.ReadFile(log.Name())
			t.Logf("sshd: %s", b)
		}
	}()
	wait(t, 5*time.Second, func() bool {
		c, e := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if e != nil {
			return false
		}
		c.Close()
		return true
	})
	hostPublic, err := os.ReadFile(filepath.Join(dir, "host.pub"))
	if err != nil {
		t.Fatal(err)
	}
	known := write("known_hosts", fmt.Sprintf("[127.0.0.1]:%d %s", port, hostPublic), 0600)
	clientConfig := write("ssh_config", fmt.Sprintf("Host fixture\n HostName 127.0.0.1\n Port %d\n User %s\n IdentityFile %s/client\n IdentitiesOnly yes\n UserKnownHostsFile %s\n", port, u.Username, dir, known), 0600)
	realSSH, _ := exec.LookPath("ssh")
	write("ssh", "#!/bin/sh\nexec "+ssh.Quote(realSSH)+" -F "+ssh.Quote(clientConfig)+" \"$@\"\n", 0700)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	servicePort := freePort(t)
	service := exec.Command("python3", "-m", "http.server", strconv.Itoa(servicePort), "--bind", "127.0.0.1", "--directory", dir)
	service.Stdout, service.Stderr = log, log
	if err := service.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { service.Process.Kill(); service.Wait() }()
	wait(t, 5*time.Second, func() bool {
		c, e := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", servicePort), 100*time.Millisecond)
		if e != nil {
			return false
		}
		c.Close()
		return true
	})
	cfg := config.Config{PollInterval: config.Duration(time.Second), CommandTimeout: config.Duration(5 * time.Second), MaxServices: 10, MaxProcesses: 4096, Hosts: []config.Host{{Name: "dev", SSH: "fixture", IncludePorts: []int{servicePort}}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d, err := daemon.Start(ctx, cfg, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); d.Wait() }()
	ep, err := ipc.Listen(filepath.Join(dir, "onji.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer ep.Close()
	served := make(chan error, 1)
	go func() { served <- ipc.Serve(ctx, ep, d, 5*time.Second) }()
	client := ipc.NewClient(filepath.Join(dir, "onji.sock"))
	defer client.Close()
	var current daemon.Service
	forwarded := func() bool {
		s, e := client.State(ctx)
		if e != nil || len(s.Hosts) != 1 {
			return false
		}
		for _, v := range s.Hosts[0].Services {
			if v.Status == "forwarded" {
				current = v
				return true
			}
		}
		return false
	}
	wait(t, 15*time.Second, forwarded)
	if current.Local == fmt.Sprintf("127.0.0.1:%d", servicePort) {
		t.Fatal("did not handle occupied local port")
	}
	httpClient := http.Client{Timeout: 3 * time.Second}
	resp, err := httpClient.Get("http://" + current.Local)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("tunnel returned %s", resp.Status)
	}
	// Repeatedly attaching and detaching clients leaves the same tunnel alive.
	for range 10 {
		c := ipc.NewClient(filepath.Join(dir, "onji.sock"))
		_, err := c.State(ctx)
		c.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := client.Forward(ctx, ipc.ForwardRequest{Host: "dev", ServiceID: current.ID, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	resp, err = httpClient.Get(fmt.Sprintf("http://127.0.0.1:%d", servicePort))
	if err != nil {
		t.Fatal("disabling tunnel stopped remote service")
	}
	resp.Body.Close()
	if c, err := net.DialTimeout("tcp", current.Local, 200*time.Millisecond); err == nil {
		c.Close()
		t.Fatal("disabled tunnel still listening")
	}
	if err := client.Forward(ctx, ipc.ForwardRequest{Host: "dev", ServiceID: current.ID, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	wait(t, 5*time.Second, forwarded)
	// End only the fixture's OpenSSH master, then wait for reconciliation.
	out, err := exec.Command("ssh", "-S", filepath.Join(dir, "h0"), "-O", "exit", "fixture").CombinedOutput()
	if err != nil {
		t.Fatalf("disconnect: %v %s", err, out)
	}
	wait(t, 5*time.Second, func() bool { _, err := os.Stat(filepath.Join(dir, "h0")); return os.IsNotExist(err) })
	wait(t, 15*time.Second, func() bool {
		if !forwarded() {
			return false
		}
		r, e := httpClient.Get("http://" + current.Local)
		if e != nil {
			return false
		}
		r.Body.Close()
		return true
	})
	ps, err := client.Processes(ctx, "dev")
	if err != nil {
		t.Fatal(err)
	}
	identity := ""
	for _, p := range ps {
		if p.PID == service.Process.Pid {
			identity = p.Identity
		}
	}
	if identity == "" {
		t.Fatal("remote service missing from process view")
	}
	q := ipc.SignalRequest{Host: "dev", PID: service.Process.Pid, Identity: identity, Signal: "TERM"}
	if err := client.Signal(ctx, q); err == nil {
		t.Fatal("termination accepted without confirmation")
	}
	q.Confirmed = true
	q.Identity = "stale"
	if err := client.Signal(ctx, q); err == nil {
		t.Fatal("termination accepted stale identity")
	}
	q.Identity = identity
	if err := client.Signal(ctx, q); err != nil {
		t.Fatal(err)
	}
	wait(t, 10*time.Second, func() bool { s, e := client.State(ctx); return e == nil && len(s.Hosts[0].Services) == 0 })
	cancel()
	d.Wait()
	select {
	case err := <-served:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("IPC shutdown hung")
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "h*")); len(files) > 0 {
		for _, file := range files {
			if strings.HasSuffix(file, "h0") {
				t.Fatal("control socket survived shutdown")
			}
		}
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}
func wait(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("condition not reached before timeout")
}
