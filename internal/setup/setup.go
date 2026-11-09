// Package setup creates the onjid configuration file.
package setup

import (
	"bufio"
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

// Run asks whether to set the config up interactively or by hand, then writes it.
// An existing file is replaced only after confirmation.
func Run(opts Options) error {
	if opts.Path == "" {
		return errors.New("config path is required")
	}
	if opts.In == nil {
		return errors.New("input is required")
	}
	if opts.Out == nil {
		opts.Out = io.Discard
	}
	in := bufio.NewReader(opts.In)
	if err := confirmReplace(opts.Path, in, opts.Out); err != nil {
		return err
	}
	mode, err := choose(in, opts.Out)
	if err != nil {
		return err
	}
	var body []byte
	switch mode {
	case "interactive", "manual":
		body = Template()
	default:
		return fmt.Errorf("unknown setup mode %q", mode)
	}
	if err := write(opts.Path, body); err != nil {
		return err
	}
	fmt.Fprintf(opts.Out, "wrote %s\n", opts.Path)
	return nil
}

func confirmReplace(path string, in *bufio.Reader, out io.Writer) error {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory", path)
	}
	answer, err := ask(in, out, path+" already exists. Replace it? [y/N]: ")
	if err != nil {
		return err
	}
	if strings.ToLower(answer) != "y" && strings.ToLower(answer) != "yes" {
		return errors.New("cancelled")
	}
	return nil
}

func choose(in *bufio.Reader, out io.Writer) (string, error) {
	fmt.Fprintln(out, "Set up the onjid config.")
	fmt.Fprintln(out, "  interactive  answer the base questions")
	fmt.Fprintln(out, "  manual       edit the template in nvim")
	for {
		answer, err := ask(in, out, "Choose [interactive/manual]: ")
		if err != nil {
			return "", err
		}
		switch strings.ToLower(answer) {
		case "i", "interactive":
			return "interactive", nil
		case "m", "manual":
			return "manual", nil
		default:
			fmt.Fprintln(out, "Enter interactive or manual.")
		}
	}
}

func ask(in *bufio.Reader, out io.Writer, prompt string) (string, error) {
	if _, err := fmt.Fprint(out, prompt); err != nil {
		return "", err
	}
	line, err := in.ReadString('\n')
	if len(line) == 0 && err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
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
