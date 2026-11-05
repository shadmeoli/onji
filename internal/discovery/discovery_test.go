package discovery

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"testing"
	"time"

	"onji/internal/config"
)

type localRunner struct{}

func (localRunner) Run(ctx context.Context, script string) ([]byte, error) {
	b, err := exec.CommandContext(ctx, "sh", "-c", script).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, b)
	}
	return b, nil
}

func TestDiscoverListenerAndRemoval(t *testing.T) {
	for _, bin := range []string{"python3", "ss"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skip(bin + " required")
		}
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	host := config.Host{IncludePorts: []int{port}}
	services, err := Scan(ctx, localRunner{}, host, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 1 || services[0].Port != port || services[0].Identity == "" {
		t.Fatalf("unexpected discovery: %+v", services)
	}
	ln.Close()
	services, err = Scan(ctx, localRunner{}, host, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 0 {
		t.Fatalf("closed service retained: %+v", services)
	}
}
