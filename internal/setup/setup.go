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
	"time"

	"onji/internal/config"
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
	case "interactive":
		var err error
		body, err = interactive(in, opts.Out)
		if err != nil {
			return err
		}
	case "manual":
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

func interactive(in *bufio.Reader, out io.Writer) ([]byte, error) {
	fmt.Fprintln(out, "Press Enter to accept a default.")
	name, err := until(in, out, "Host name [dev]: ", func(answer string) (string, error) {
		if answer == "" {
			answer = "dev"
		}
		if err := checkHost(answer, "dev", "all", nil, []int{22}); err != nil {
			return "", err
		}
		return answer, nil
	})
	if err != nil {
		return nil, err
	}
	target, err := until(in, out, "SSH alias or user@host: ", func(answer string) (string, error) {
		if answer == "" {
			return "", errors.New("an SSH alias or user@host is required")
		}
		if err := checkHost(name, answer, "all", nil, []int{22}); err != nil {
			return "", err
		}
		return answer, nil
	})
	if err != nil {
		return nil, err
	}
	discovery, err := until(in, out, "Discovery (all or new) [all]: ", func(answer string) (string, error) {
		if answer == "" {
			answer = "all"
		}
		if answer != "all" && answer != "new" {
			return "", errors.New("discovery must be all or new")
		}
		return answer, nil
	})
	if err != nil {
		return nil, err
	}
	forward := true
	for {
		answer, err := ask(in, out, "Automatically forward discovered ports? [Y/n]: ")
		if err != nil {
			return nil, err
		}
		switch strings.ToLower(answer) {
		case "", "y", "yes":
			forward = true
		case "n", "no":
			forward = false
		default:
			fmt.Fprintln(out, "Enter yes or no.")
			continue
		}
		break
	}
	include, err := askPorts(in, out, "Include ports (comma-separated, empty discovers all): ", nil, func(ports []int) error {
		return checkHost(name, target, discovery, ports, []int{22})
	})
	if err != nil {
		return nil, err
	}
	exclude, err := askPorts(in, out, `Exclude ports [22] ("none" excludes nothing): `, []int{22}, func(ports []int) error {
		return checkHost(name, target, discovery, include, ports)
	})
	if err != nil {
		return nil, err
	}
	doc := example()
	doc.Hosts[0] = host{
		Name:         name,
		SSH:          target,
		Discovery:    discovery,
		AutoForward:  forward,
		IncludePorts: append([]int{}, include...),
		ExcludePorts: append([]int{}, exclude...),
	}
	body, err := marshal(doc)
	if err != nil {
		return nil, err
	}
	if err := validate(body); err != nil {
		return nil, err
	}
	return body, nil
}

func until(in *bufio.Reader, out io.Writer, prompt string, parse func(string) (string, error)) (string, error) {
	for {
		answer, err := ask(in, out, prompt)
		if err != nil {
			return "", err
		}
		value, err := parse(answer)
		if err != nil {
			fmt.Fprintln(out, err)
			continue
		}
		return value, nil
	}
}

func askPorts(in *bufio.Reader, out io.Writer, prompt string, empty []int, check func([]int) error) ([]int, error) {
	for {
		answer, err := ask(in, out, prompt)
		if err != nil {
			return nil, err
		}
		var ports []int
		switch {
		case answer == "":
			ports = append([]int{}, empty...)
		case strings.EqualFold(answer, "none"):
			ports = []int{}
		default:
			ports, err = parsePorts(answer)
			if err != nil {
				fmt.Fprintln(out, err)
				continue
			}
		}
		if err := check(ports); err != nil {
			fmt.Fprintln(out, err)
			continue
		}
		return ports, nil
	}
}

func parsePorts(value string) ([]int, error) {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || strings.ContainsRune(" \t", r)
	})
	ports := make([]int, 0, len(parts))
	for _, part := range parts {
		port, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("invalid port %q", part)
		}
		ports = append(ports, port)
	}
	if len(ports) == 0 {
		return nil, errors.New("enter at least one port")
	}
	return ports, nil
}

func checkHost(name, target, discovery string, include, exclude []int) error {
	cfg := config.Config{
		PollInterval:   config.Duration(5 * time.Second),
		CommandTimeout: config.Duration(15 * time.Second),
		MaxServices:    256,
		MaxProcesses:   2048,
		Hosts: []config.Host{{
			Name:         name,
			SSH:          target,
			Discovery:    discovery,
			IncludePorts: include,
			ExcludePorts: exclude,
		}},
	}
	return cfg.Validate()
}

func validate(body []byte) error {
	dir, err := os.MkdirTemp("", "onji-config-check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return err
	}
	_, err = config.Load(path)
	return err
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
