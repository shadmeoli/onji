package ssh

import "os/exec"

func execShell(script string) ([]byte, error) { return exec.Command("sh", "-c", script).Output() }
