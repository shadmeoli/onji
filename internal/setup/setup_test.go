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
	if err := Run(Options{Path: path, In: strings.NewReader(""), Out: &out}); err != nil {
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
	if err := Run(Options{Path: path, In: strings.NewReader(""), Out: &out}); err == nil {
		t.Fatal("existing config was replaced")
	}
	again, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, Template()) {
		t.Fatal("existing config changed")
	}
}
