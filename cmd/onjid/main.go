package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"onji/internal/config"
	"onji/internal/daemon"
	"onji/internal/ipc"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "onjid:", err)
		os.Exit(1)
	}
}
func run() error {
	path := flag.String("config", config.DefaultPath(), "configuration file")
	socket := flag.String("socket", config.SocketPath(), "private Unix socket path")
	check := flag.Bool("check", false, "validate configuration and exit")
	flag.Parse()
	if flag.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flag.Args())
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	if *check {
		fmt.Println("Configuration is valid.")
		return nil
	}
	endpoint, err := ipc.Listen(*socket)
	if err != nil {
		return err
	}
	defer endpoint.Close()
	dir, err := os.MkdirTemp(filepath.Dir(*socket), "ssh-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	d, err := daemon.Start(ctx, cfg, dir)
	if err != nil {
		return err
	}
	slog.Info("Onji daemon started", "socket", *socket, "hosts", len(cfg.Hosts))
	err = ipc.Serve(ctx, endpoint, d, time.Duration(cfg.CommandTimeout))
	cancel()
	d.Wait()
	slog.Info("Onji daemon stopped")
	return err
}
