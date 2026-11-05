package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

type Duration time.Duration

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	*d = Duration(v)
	return err
}

type Host struct {
	Name         string `json:"name"`
	SSH          string `json:"ssh"`
	Discovery    string `json:"discovery"`
	AutoForward  *bool  `json:"auto_forward,omitempty"`
	IncludePorts []int  `json:"include_ports,omitempty"`
	ExcludePorts []int  `json:"exclude_ports,omitempty"`
}

func (h Host) Automatic() bool { return h.AutoForward == nil || *h.AutoForward }

type Config struct {
	PollInterval   Duration `json:"poll_interval"`
	CommandTimeout Duration `json:"command_timeout"`
	MaxServices    int      `json:"max_services_per_host"`
	MaxProcesses   int      `json:"max_processes_per_host"`
	Hosts          []Host   `json:"hosts"`
}

func DefaultPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "onji.json"
	}
	return filepath.Join(dir, "onji", "config.json")
}

func RuntimeDir() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(dir) {
		return filepath.Join(dir, "onji")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("onji-%d", os.Getuid()))
}

func SocketPath() string { return filepath.Join(RuntimeDir(), "onji.sock") }

func Load(path string) (Config, error) {
	c := Config{PollInterval: Duration(5 * time.Second), CommandTimeout: Duration(15 * time.Second), MaxServices: 256, MaxProcesses: 2048}
	f, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return c, errors.New("config must contain exactly one JSON object")
	}
	return c, c.Validate()
}

var nameRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)
var sshRE = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_@.:%+\[\]-]{0,253}$`)

func (c *Config) Validate() error {
	if len(c.Hosts) == 0 || len(c.Hosts) > 32 {
		return errors.New("configure between 1 and 32 hosts")
	}
	if c.PollInterval < Duration(time.Second) || c.PollInterval > Duration(time.Hour) {
		return errors.New("poll_interval must be between 1s and 1h")
	}
	if c.CommandTimeout < Duration(time.Second) || c.CommandTimeout > Duration(2*time.Minute) {
		return errors.New("command_timeout must be between 1s and 2m")
	}
	if c.MaxServices < 1 || c.MaxServices > 512 {
		return errors.New("max_services_per_host must be between 1 and 512")
	}
	if c.MaxProcesses < 1 || c.MaxProcesses > 4096 {
		return errors.New("max_processes_per_host must be between 1 and 4096")
	}
	names := map[string]bool{}
	for i := range c.Hosts {
		h := &c.Hosts[i]
		if !nameRE.MatchString(h.Name) || names[h.Name] {
			return fmt.Errorf("invalid or duplicate host name %q", h.Name)
		}
		names[h.Name] = true
		if !sshRE.MatchString(h.SSH) {
			return fmt.Errorf("host %s: ssh must be an SSH config alias or user@hostname", h.Name)
		}
		if h.Discovery == "" {
			h.Discovery = "all"
		}
		if h.Discovery != "all" && h.Discovery != "new" {
			return fmt.Errorf("host %s: discovery must be all or new", h.Name)
		}
		if h.ExcludePorts == nil {
			h.ExcludePorts = []int{22}
		}
		for _, ports := range [][]int{h.IncludePorts, h.ExcludePorts} {
			if len(ports) > 1024 {
				return errors.New("too many port rules")
			}
			for _, p := range ports {
				if p < 1 || p > 65535 {
					return fmt.Errorf("invalid port %d", p)
				}
			}
		}
	}
	return nil
}
