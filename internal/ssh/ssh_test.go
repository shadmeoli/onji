package ssh

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOutputIsBoundedAndTimeoutReapsChild(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ssh")
	script := "#!/bin/sh\nif [ \"$1\" = flood ]; then head -c 3000000 /dev/zero; else sleep 60; fi\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := run(ctx, "flood"); err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("output not bounded: %v", err)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel2()
	start := time.Now()
	if _, err := run(ctx2, "block"); err == nil {
		t.Fatal("blocking child succeeded")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("cancellation failed to reap child and close inherited pipes")
	}
}

func TestQuoteShellMetacharacters(t *testing.T) {
	value := "abc' $(printf injected); \nxyz"
	// A round trip through a shell verifies that quotes and substitutions
	// remain argument data. No part of value is interpolated unquoted.
	got, err := execShell("printf %s " + Quote(value))
	if err != nil || string(got) != value {
		t.Fatalf("quote round trip: %q %v", got, err)
	}
}
