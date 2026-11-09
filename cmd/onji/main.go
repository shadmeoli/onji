package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"unicode"

	"onji/internal/config"
	"onji/internal/ipc"
	"onji/internal/setup"
	"onji/internal/tui"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "onji:", err)
		os.Exit(1)
	}
}
func run() error {
	return runArgs(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
}

func runArgs(args []string, in io.Reader, out, errw io.Writer) error {
	fs := flag.NewFlagSet("onji", flag.ContinueOnError)
	fs.SetOutput(errw)
	socket := fs.String("socket", config.SocketPath(), "daemon Unix socket path")
	configInit := fs.Bool("config-init", false, "set up the onjid configuration and exit")
	fs.Usage = func() {
		fmt.Fprintln(errw, `Usage: onji [--socket PATH] [--config-init] [command]

--config-init asks whether to set up the onjid config interactively or by hand,
then writes it to the default path and exits. It does not need a running daemon.
An existing file is replaced only after confirmation.

Commands:
  tui                              Open the terminal UI (default)
  status [--json]                   List hosts and forwarded services
  processes HOST                   List remote processes owned by the SSH user
  forward HOST SERVICE_ID on|off    Enable or disable a tunnel
  stop HOST PID [--force] [--yes]   Confirm and send TERM (or KILL)

Use the exact service ID shown by status, for example 127.0.0.1:3000.
Closing onji leaves onjid and its tunnels running.`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *configInit {
		if fs.NArg() != 0 {
			return errors.New("usage: onji --config-init")
		}
		return setup.Run(setup.Options{Path: config.DefaultPath(), In: in, Out: out})
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	client := ipc.NewClient(*socket)
	defer client.Close()
	args = fs.Args()
	if len(args) == 0 {
		args = []string{"tui"}
	}
	switch args[0] {
	case "tui":
		if len(args) != 1 {
			return errors.New("usage: onji tui")
		}
		return tui.Run(ctx, client)
	case "status":
		if len(args) > 2 || (len(args) == 2 && args[1] != "--json") {
			return errors.New("usage: onji status [--json]")
		}
		s, err := client.State(ctx)
		if err != nil {
			return err
		}
		if len(args) == 2 {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(s)
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "HOST\tPROCESS\tPID\tSERVICE ID\tLOCAL\tSTATUS")
		for _, h := range s.Hosts {
			if len(h.Services) == 0 {
				fmt.Fprintf(w, "%s\t—\t—\t—\t—\t%s\n", h.Name, h.Status)
			}
			for _, v := range h.Services {
				fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t%s\n", h.Name, clean(v.Name), v.PID, clean(v.ID), v.Local, v.Status)
			}
			if h.Error != "" {
				fmt.Fprintln(os.Stderr, h.Name+": "+clean(h.Error))
			}
		}
		return w.Flush()
	case "processes":
		if len(args) != 2 {
			return errors.New("usage: onji processes HOST")
		}
		p, err := client.Processes(ctx, args[1])
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "PID\tPROCESS\tUID")
		for _, v := range p {
			fmt.Fprintf(w, "%d\t%s\t%d\n", v.PID, clean(v.Name), v.UID)
		}
		return w.Flush()
	case "forward":
		if len(args) != 4 || (args[3] != "on" && args[3] != "off") {
			return errors.New("usage: onji forward HOST SERVICE_ID on|off")
		}
		return client.Forward(ctx, ipc.ForwardRequest{Host: args[1], ServiceID: args[2], Enabled: args[3] == "on"})
	case "stop":
		if len(args) < 3 {
			return errors.New("usage: onji stop HOST PID [--force] [--yes]")
		}
		pid, err := strconv.Atoi(args[2])
		if err != nil || pid <= 1 {
			return errors.New("PID must be greater than 1")
		}
		force, yes := false, false
		for _, arg := range args[3:] {
			switch arg {
			case "--force":
				force = true
			case "--yes":
				yes = true
			default:
				return fmt.Errorf("unknown stop option %q", arg)
			}
		}
		p, err := client.Processes(ctx, args[1])
		if err != nil {
			return err
		}
		for _, v := range p {
			if v.PID == pid {
				action := "TERM"
				if force {
					action = "KILL"
				}
				if !yes {
					info, err := os.Stdin.Stat()
					if err != nil {
						return err
					}
					if info.Mode()&os.ModeCharDevice == 0 {
						return errors.New("non-interactive termination requires --yes")
					}
					fmt.Fprintf(os.Stderr, "Send SIG%s to %s (PID %d) on %s? Type yes: ", action, clean(v.Name), pid, clean(args[1]))
					line, err := bufio.NewReader(os.Stdin).ReadString('\n')
					if err != nil {
						return err
					}
					if strings.TrimSpace(line) != "yes" {
						return errors.New("cancelled")
					}
				}
				return client.Signal(ctx, ipc.SignalRequest{Host: args[1], PID: pid, Identity: v.Identity, Signal: action, Confirmed: true})
			}
		}
		return errors.New("process not found among processes owned by the SSH user")
	default:
		return fmt.Errorf("unknown command %q; use onji --help", args[0])
	}
}
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}
