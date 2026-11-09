package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"onji/internal/setup"
)

func TestConfigInitCommand(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	var stdout, stderr bytes.Buffer
	if err := runArgs([]string{"--config-init"}, strings.NewReader("interactive\n"), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "onji", "config.json")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, setup.Template()) {
		t.Fatalf("wrote:\n%s", got)
	}
	if !strings.Contains(stdout.String(), path) {
		t.Fatalf("stdout %q", stdout.String())
	}
	if err := runArgs([]string{"--config-init", "extra"}, strings.NewReader(""), &stdout, &stderr); err == nil {
		t.Fatal("extra arguments accepted")
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := runArgs([]string{"--config-init"}, strings.NewReader("n\n"), &stdout, &stderr); err == nil {
		t.Fatal("existing config replaced")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("existing config changed")
	}
}

func TestHelpExitsCleanly(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := runArgs([]string{"--help"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "--config-init") {
		t.Fatalf("help:\n%s", stderr.String())
	}
}
