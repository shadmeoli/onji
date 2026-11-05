package process

import (
	"context"
	"fmt"
	"os/exec"
	"testing"
	"time"
)

type localRunner struct{}

func (localRunner) Run(ctx context.Context, script string) ([]byte, error) {
	c := exec.CommandContext(ctx, "sh", "-c", script)
	b, err := c.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, b)
	}
	return b, nil
}

func TestSignalRevalidatesIdentityAndOnlyStopsSelectedChild(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, action := range []string{"TERM", "KILL"} {
		t.Run(action, func(t *testing.T) {
			child := exec.Command("sleep", "60")
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { child.Process.Kill(); child.Wait() }()
			ps, err := List(ctx, localRunner{}, 4096)
			if err != nil {
				t.Fatal(err)
			}
			var identity string
			for _, p := range ps {
				if p.PID == child.Process.Pid {
					identity = p.Identity
				}
			}
			if identity == "" {
				t.Fatal("child missing from process list")
			}
			if err := Signal(ctx, localRunner{}, child.Process.Pid, "wrong-identity", action); err == nil {
				t.Fatal("accepted stale identity")
			}
			if err := Signal(ctx, localRunner{}, child.Process.Pid, identity, action); err != nil {
				t.Fatal(err)
			}
		})
	}
}
