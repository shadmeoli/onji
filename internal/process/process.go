package process

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"strconv"

	"onji/internal/ssh"
)

// Prelude is shared by the remote process and listening-service inspectors.
//
//go:embed common.py
var Prelude string

//go:embed list.py
var listScript string

//go:embed terminate.py
var signalScript string

type Info struct {
	PID      int    `json:"pid"`
	Name     string `json:"name"`
	Identity string `json:"identity"`
	UID      int    `json:"uid"`
}

func List(ctx context.Context, r ssh.Runner, limit int) ([]Info, error) {
	b, err := ssh.Python(ctx, r, Prelude+"\n"+listScript, strconv.Itoa(limit))
	if err != nil {
		return nil, err
	}
	var result []Info
	if err := json.Unmarshal(b, &result); err != nil {
		return nil, err
	}
	if len(result) > limit {
		return nil, errors.New("remote process limit exceeded")
	}
	return result, nil
}

func Signal(ctx context.Context, r ssh.Runner, pid int, identity, action string) error {
	if pid <= 1 || identity == "" || len(identity) > 128 || (action != "TERM" && action != "KILL") {
		return errors.New("valid PID, identity, and TERM or KILL signal required")
	}
	_, err := ssh.Python(ctx, r, Prelude+"\n"+signalScript, strconv.Itoa(pid), identity, action)
	return err
}
