package setup

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"onji/internal/config"
)

func TestTemplateMatchesExampleAndLoads(t *testing.T) {
	got := Template()
	want, err := os.ReadFile(filepath.Join("..", "..", "config.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("template:\n%s", got)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, got, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path); err != nil {
		t.Fatal(err)
	}
}

func TestRunWritesTemplateOnce(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config", "onji")
	path := filepath.Join(dir, "config.json")
	var out bytes.Buffer
	if err := Run(Options{Path: path, In: strings.NewReader("manual\n"), Out: &out}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, Template()) {
		t.Fatalf("wrote:\n%s", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %o", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode %o", dirInfo.Mode().Perm())
	}
	if !strings.Contains(out.String(), path) {
		t.Fatalf("output %q", out.String())
	}
	out.Reset()
	if err := Run(Options{Path: path, In: strings.NewReader("\n"), Out: &out}); err == nil || !strings.Contains(out.String(), "Replace it?") {
		t.Fatal(err)
	}
	again, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, Template()) {
		t.Fatal("existing config changed")
	}
}

func TestRunAcceptsEitherSetupChoice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "onji", "config.json")
	var out bytes.Buffer
	in := "nope\nmanual\n"
	if err := Run(Options{Path: path, In: strings.NewReader(in), Out: &out}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Enter interactive or manual.") {
		t.Fatalf("output %q", out.String())
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, Template()) {
		t.Fatalf("wrote:\n%s", got)
	}
	out.Reset()
	replace := "yes\ni\n\nuser@dev\n\n\n\n\n"
	if err := Run(Options{Path: path, In: strings.NewReader(replace), Out: &out}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "wrote "+path) {
		t.Fatalf("output %q", out.String())
	}
}

func TestInteractiveSetup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "onji", "config.json")
	input := strings.Join([]string{
		"interactive",
		" ",
		"bad ssh;",
		"user@dev",
		"later",
		"new",
		"maybe",
		"n",
		"99999",
		"3000, 5173",
		"none",
	}, "\n") + "\n"
	var out bytes.Buffer
	if err := Run(Options{Path: path, In: strings.NewReader(input), Out: &out}); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "ssh must be") || !strings.Contains(text, "discovery must be all or new") || !strings.Contains(text, "Enter yes or no.") || !strings.Contains(text, "invalid port 99999") {
		t.Fatalf("output %q", text)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	host := cfg.Hosts[0]
	if host.Name != "dev" || host.SSH != "user@dev" || host.Discovery != "new" || host.Automatic() {
		t.Fatalf("%+v", host)
	}
	if len(host.IncludePorts) != 2 || host.IncludePorts[0] != 3000 || host.IncludePorts[1] != 5173 || len(host.ExcludePorts) != 0 {
		t.Fatalf("ports include=%v exclude=%v", host.IncludePorts, host.ExcludePorts)
	}
}
