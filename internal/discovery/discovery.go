package discovery

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"strings"

	"onji/internal/config"
	"onji/internal/process"
	"onji/internal/ssh"
)

//go:embed listeners.py
var script string

type Listener struct {
	Address  string `json:"address"`
	Port     int    `json:"port"`
	PID      int    `json:"pid"`
	Name     string `json:"name"`
	Identity string `json:"identity"`
}

func (l Listener) ID() string { return net.JoinHostPort(l.Address, strconv.Itoa(l.Port)) }
func (l Listener) Destination() string {
	if l.Address == "0.0.0.0" {
		return "127.0.0.1"
	}
	if l.Address == "::" {
		return "::1"
	}
	return l.Address
}

func Scan(ctx context.Context, r ssh.Runner, host config.Host, limit int) ([]Listener, error) {
	include, _ := json.Marshal(append([]int{}, host.IncludePorts...))
	exclude, _ := json.Marshal(append([]int{}, host.ExcludePorts...))
	b, err := ssh.Python(ctx, r, process.Prelude+"\n"+script, strconv.Itoa(limit), string(include), string(exclude))
	if err != nil {
		return nil, err
	}
	var result []Listener
	if err := json.Unmarshal(b, &result); err != nil {
		return nil, err
	}
	if len(result) > limit {
		return nil, errors.New("remote service limit exceeded")
	}
	for _, l := range result {
		address, _, _ := strings.Cut(l.Address, "%")
		if net.ParseIP(address) == nil || l.Port < 1 || l.Port > 65535 || strings.ContainsAny(l.Address, "\r\n[] ") {
			return nil, errors.New("invalid remote listening address")
		}
	}
	return result, nil
}
