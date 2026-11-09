// Package setup creates the onjid configuration file.
package setup

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Options controls where the config is written and where messages go.
type Options struct {
	Path string
	In   io.Reader
	Out  io.Writer
}

type document struct {
	PollInterval   string `json:"poll_interval"`
	CommandTimeout string `json:"command_timeout"`
	MaxServices    int    `json:"max_services_per_host"`
	MaxProcesses   int    `json:"max_processes_per_host"`
	Hosts          []host `json:"hosts"`
}

type host struct {
	Name         string `json:"name"`
	SSH          string `json:"ssh"`
	Discovery    string `json:"discovery"`
	AutoForward  bool   `json:"auto_forward"`
	IncludePorts []int  `json:"include_ports"`
	ExcludePorts []int  `json:"exclude_ports"`
}

// Template is the editable starting config, matching config.example.json.
func Template() []byte {
	body, err := marshal(example())
	if err != nil {
		panic(err)
	}
	return body
}

func example() document {
	return document{
		PollInterval:   "5s",
		CommandTimeout: "15s",
		MaxServices:    256,
		MaxProcesses:   2048,
		Hosts: []host{{
			Name:         "dev",
			SSH:          "my-dev-server",
			Discovery:    "all",
			AutoForward:  true,
			IncludePorts: []int{3000, 5173, 8000, 8080},
			ExcludePorts: []int{22},
		}},
	}
}

func marshal(doc document) ([]byte, error) {
	if len(doc.Hosts) != 1 {
		return nil, errors.New("the starter config contains one host")
	}
	h := doc.Hosts[0]
	name, err := json.Marshal(h.Name)
	if err != nil {
		return nil, err
	}
	target, err := json.Marshal(h.SSH)
	if err != nil {
		return nil, err
	}
	discovery, err := json.Marshal(h.Discovery)
	if err != nil {
		return nil, err
	}
	body := fmt.Sprintf(`{
  "poll_interval": %q,
  "command_timeout": %q,
  "max_services_per_host": %d,
  "max_processes_per_host": %d,
  "hosts": [
    {
      "name": %s,
      "ssh": %s,
      "discovery": %s,
      "auto_forward": %t,
      "include_ports": %s,
      "exclude_ports": %s
    }
  ]
}
`, doc.PollInterval, doc.CommandTimeout, doc.MaxServices, doc.MaxProcesses, name, target, discovery, h.AutoForward, formatPorts(h.IncludePorts), formatPorts(h.ExcludePorts))
	return []byte(body), nil
}

func formatPorts(ports []int) string {
	if len(ports) == 0 {
		return "[]"
	}
	parts := make([]string, len(ports))
	for i, port := range ports {
		parts[i] = strconv.Itoa(port)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// Run writes the config template. An existing file is left unchanged.
func Run(opts Options) error {
	if opts.Path == "" {
		return errors.New("config path is required")
	}
	if opts.Out == nil {
		opts.Out = io.Discard
	}
	info, err := os.Stat(opts.Path)
	if err == nil {
		if info.IsDir() {
			return fmt.Errorf("%s is a directory", opts.Path)
		}
		return fmt.Errorf("config already exists: %s", opts.Path)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := write(opts.Path, Template()); err != nil {
		return err
	}
	fmt.Fprintf(opts.Out, "wrote %s\n", opts.Path)
	return nil
}

func write(path string, body []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	f, err := os.CreateTemp(dir, ".config.json.*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	done := false
	defer func() {
		if !done {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	done = true
	return nil
}
