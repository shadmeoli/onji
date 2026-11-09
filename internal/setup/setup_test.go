package setup

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
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
	if err := Run(Options{Path: path, In: strings.NewReader("manual\n"), Out: &out, Editor: keepTemplate}); err != nil {
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
	if err := Run(Options{Path: path, In: strings.NewReader(in), Out: &out, Editor: keepTemplate}); err != nil {
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

func keepTemplate(string) error { return nil }

func TestManualInstallsTheEditedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "onji", "config.json")
	var draft string
	var edited []byte
	editor := func(filename string) error {
		draft = filename
		body, err := os.ReadFile(filename)
		if err != nil {
			return err
		}
		if !bytes.Equal(body, Template()) {
			t.Fatalf("opened:\n%s", body)
		}
		edited = bytes.Replace(body, []byte("my-dev-server"), []byte("user@dev"), 1)
		return os.WriteFile(filename, edited, 0o600)
	}
	var out bytes.Buffer
	if err := Run(Options{Path: path, In: strings.NewReader("m\n"), Out: &out, Editor: editor}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, edited) {
		t.Fatalf("installed:\n%s", got)
	}
	if _, err := config.Load(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(draft); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("draft remained: %v", err)
	}
}

func TestManualDoesNotInstallAnInvalidEdit(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "onji")
	path := filepath.Join(dir, "config.json")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, Template(), 0o600); err != nil {
		t.Fatal(err)
	}
	var draft string
	editor := func(filename string) error {
		draft = filename
		return os.WriteFile(filename, []byte("{}\n"), 0o600)
	}
	err := Run(Options{Path: path, In: strings.NewReader("yes\nmanual\n"), Out: ioDiscard(), Editor: editor})
	if err == nil || !strings.Contains(err.Error(), "draft kept at "+draft) {
		t.Fatal(err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(got, Template()) {
		t.Fatal("existing config was replaced")
	}
	if _, statErr := os.Stat(draft); statErr != nil {
		t.Fatal(statErr)
	}
	t.Cleanup(func() { _ = os.Remove(draft) })
}

func TestManualEditorFailureLeavesNoConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "onji", "config.json")
	err := Run(Options{Path: path, In: strings.NewReader("manual\n"), Out: ioDiscard(), Editor: func(string) error {
		return errors.New("quit")
	}})
	if err == nil || !strings.Contains(err.Error(), "quit") {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatal(statErr)
	}
}

func TestOpenNvimRequiresATerminalAndRunsNvim(t *testing.T) {
	pipeRead, pipeWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	restore := func() {
		editorStdin = os.Stdin
		lookPath = exec.LookPath
		command = exec.Command
	}
	t.Cleanup(func() {
		_ = pipeRead.Close()
		_ = pipeWrite.Close()
		restore()
	})
	editorStdin = pipeRead
	lookPath = func(string) (string, error) { return "", errors.New("missing") }
	if err := openNvim("config.json"); err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatal(err)
	}

	tty, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tty.Close() })
	editorStdin = tty
	if err := openNvim("config.json"); err == nil || !strings.Contains(err.Error(), "nvim is not installed") {
		t.Fatal(err)
	}
	called := false
	lookPath = func(name string) (string, error) {
		if name != "nvim" {
			t.Fatalf("looked up %s", name)
		}
		return "/usr/bin/nvim", nil
	}
	command = func(name string, args ...string) *exec.Cmd {
		called = true
		if name != "nvim" || len(args) != 1 || args[0] != "config.json" {
			t.Fatalf("command %s %v", name, args)
		}
		return exec.Command("true")
	}
	if err := openNvim("config.json"); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("nvim was not started")
	}
}

func ioDiscard() *bytes.Buffer { return &bytes.Buffer{} }
