# Onji

Automatic SSH port forwarding with an independent Go daemon and an optional
Bubble Tea terminal UI. Start a service on a configured SSH host; Onji discovers
its listening port and forwards it locally. Detaching the TUI leaves the daemon
and its tunnels running.

## Requirements

- **Local:** Linux, Go 1.26+ to build, and OpenSSH with multiplexing.
- **Remote:** Linux with `/proc`, Python 3.9+, and `ss` from iproute2. Safe process
  termination requires `pidfd_open` support (Linux 5.3+).
- Non-interactive SSH authentication and an already trusted host key. Onji uses
  your SSH config, keys/agent, and jump hosts. It does not prompt for passwords
  or accept unknown host keys.

Small Python inspection scripts run over SSH. Onji installs no remote daemon or
packages. Other operating systems are not supported in this version.

## Quick start

```sh
make build
./bin/onji --config-init
```

This asks you to choose interactive or manual setup. Interactive setup asks for
one host: its name, SSH target, discovery mode, automatic forwarding, and ports.
Manual setup opens the starter template in nvim. Onji checks the saved file and
writes it to the default config path. An invalid edit is not installed, and the
draft path is printed. An existing file is replaced only after you confirm.
Verify authentication and host trust with `ssh YOUR_ALIAS true`, then start the
daemon:

```sh
./bin/onjid --check
./bin/onjid
```

In another terminal, open the TUI with `./bin/onji`.

`onjid` stays in the foreground so a supervisor can manage it. It has no
background flag. The next section runs this same binary in the background.

## Run onjid in the background

Use the systemd user unit in `packaging/systemd/onjid.service`. It starts
`~/.local/bin/onjid`, restarts it 5 seconds after a failure, and stops the SSH
children when the daemon stops. No `onjid` or `onji` command change is involved.

```sh
make install
mkdir -p ~/.config/systemd/user
cp packaging/systemd/onjid.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now onjid
```

`make install` puts the binaries in `~/.local/bin`. The unit reads the default
config path. Check it, restart it after a config edit, read its log, or stop it:

```sh
systemctl --user status onjid
systemctl --user restart onjid
journalctl --user -u onjid -e
systemctl --user stop onjid
```

Run only one daemon. A second `onjid`, including one left in a terminal, exits
with `another onjid instance is using this socket`, and systemd keeps retrying.
Press Ctrl+C in that terminal, then run `systemctl --user restart onjid`.

A user service stops when your login session ends. To keep the tunnels after
logout:

```sh
sudo loginctl enable-linger "$USER"
```

## Logging

`onji status` is the current snapshot. A host error disappears from it when the
next poll succeeds, so status is not a history of what happened.

`onjid` writes `slog` to stderr. The lines it writes today are daemon start and
daemon stop. A foreground `onjid` prints them in that terminal. Under the
systemd unit, read the same stream with:

```sh
journalctl --user -u onjid -e
```

These events belong in that log when they happen:

- SSH connect and disconnect, including the error text and the backoff before the next try.
- A scan failure that drops that host's tunnels.
- A forward created, removed, or moved because the preferred local port was taken.
- The process exiting because another `onjid` already holds the socket.

A successful poll stays out of the log. Forwarded traffic stays out too: those
bytes are carried by OpenSSH and never enter the Go process.

## Configuration

The default config is `$XDG_CONFIG_HOME/onji/config.json`, or
`~/.config/onji/config.json`. Override it with `onjid --config PATH`.

```json
{
  "poll_interval": "5s",
  "command_timeout": "15s",
  "max_services_per_host": 256,
  "max_processes_per_host": 2048,
  "hosts": [
    {
      "name": "dev",
      "ssh": "my-dev-server",
      "discovery": "all",
      "auto_forward": true,
      "include_ports": [3000, 5173, 8000, 8080],
      "exclude_ports": [22]
    }
  ]
}
```

- All configured hosts are monitored while the daemon runs. Restart it to apply
  config changes.
- `discovery: "all"` includes existing and newly started services.
- `discovery: "new"` displays existing services but only automatically forwards
  endpoints absent from the first successful scan. This baseline survives
  reconnects and resets on daemon restart.
- Omit `include_ports` (or use `[]`) to discover all listening TCP ports.
  Exclusions take precedence. Port 22 is excluded by default; an explicit
  `exclude_ports: []` removes that default exclusion.
- `auto_forward: false` discovers services for manual forwarding. The default
  is `true`.
- Forwarding overrides survive reconnects, but reset when an endpoint disappears
  in a successful scan or the daemon restarts. They are not saved to disk.
- Forwards bind to **127.0.0.1 only**. Onji tries the remote port number first,
  then chooses another local port on collision. The UI displays the actual
  address. Wildcard remote listeners use remote loopback as their destination.
- Exceeding a discovery limit produces a host error rather than a truncated
  list. Narrow port rules or adjust the configured limit.

The socket defaults to `$XDG_RUNTIME_DIR/onji/onji.sock`, falling back to
`/tmp/onji-UID/onji.sock`. Both commands accept `--socket PATH`. Its containing
directory must be owned by you with mode `0700`; the socket has mode `0600`.

## TUI and CLI

| Key | Action |
| --- | --- |
| Up/down or j/k | Select a row |
| `/` | Filter by host, process, PID, or address |
| Enter/Escape while filtering | Return to navigation |
| Escape while navigating | Clear the filter |
| `f` | Toggle the selected service's forwarding |
| `p` or Tab | Switch between services and remote processes |
| `[` / `]` in process view | Switch host |
| `x` | Request SIGTERM, then confirm with `y` |
| `X` | Request SIGKILL, then confirm with `y` |
| `q` or Ctrl+C | Detach; leave the daemon running |

```sh
onji status
onji status --json
onji processes dev
onji forward dev 127.0.0.1:3000 off
onji forward dev 127.0.0.1:3000 on
onji stop dev 12345
onji stop dev 12345 --force
```

Use the exact service ID shown in `status`. Place `--socket` before the command.
Termination prompts for confirmation; scripts must explicitly supply `--yes`.
Sending a signal reports acceptance, not a guarantee that the process exited.
A supervisor may restart it.

**Removing a tunnel never terminates the application.** The process view includes
applications without listening ports. Controls target individual processes owned
by the SSH user. Process groups, service-manager integration, and tmux/screen
session labels are not implemented. Listening services inside those sessions are
discovered without attaching. Process metadata is shown when permissions allow;
shared listeners show one available owning process.

## Architecture and long-running operation

```text
cmd/
  onji/       CLI and Bubble Tea TUI entry point
  onjid/      Daemon entry point
internal/
  config/     Host settings and discovery rules
  daemon/     Per-host supervision, reconciliation, and snapshots
  discovery/  Linux listening-port inspection
  ipc/        Versioned HTTP API over a private Unix socket
  process/    Process listing and identity-checked signals through pidfds
  ssh/        OpenSSH subprocess and control-connection ownership
  tunnel/     Forward creation, collision handling, and removal
  tui/        Bubble Tea, Bubbles, and Lip Gloss interface
integration/  Isolated real-SSH lifecycle test
packaging/    Optional systemd user unit
```

OpenSSH carries forwarded traffic; Go handles discovery and management. Go GC
does not directly pause established tunnel traffic. Each host has one serial
worker, a bounded command queue, and one owned SSH master. Polls never overlap
within a host. Failed connections back off and reconcile on recovery. Shutdown
cancels work, reaps SSH children, and removes control sockets.

State holds current services rather than event history. IPC connections, request
bodies, command output, and discovered records are bounded. The TUI polls with
one refresh loop, so no subscription backlog builds up while detached.
`onji status --json` includes heap, allocation, goroutine, and cumulative GC CPU
counters.

Go's GC defaults are retained. You can set `GOMEMLIMIT` after measuring the
workload; it is a soft Go-runtime limit, excludes SSH children, and does not cap
total RSS. Periodic forced GC or restarts are not part of normal operation.

## Versioning

Tags use `v<major>.<major-addition>.<patch>`:

- **major** is a break that existing setups cannot keep.
- **major-addition** is a capability existing setups can keep.
- **patch** is a fix or bug correction.

`v0.0.1` is major 0, addition 0, patch 1. The same commit is published on
`master` and `beta`. Git keeps one tag named `v0.0.1`, so the beta channel uses
the prerelease tag `v0.0.1-beta`.

## Development and verification

```sh
make check                         # formatting, vet, race tests, builds
make integration                   # private loopback sshd; no external hosts
make soak SOAK_DURATION=30s SOAK_TIMEOUT=1m
make soak                          # 48-hour churn test, 49-hour timeout
```

Integration tests require `sshd`, `ssh-keygen`, `ss`, Python 3.9+, and the SSH
server's privilege-separation directory (typically `/run/sshd`). They use
temporary keys and do not modify `~/.ssh`. Coverage includes real forwarding,
port collisions, client detach, reconnect, confirmation, process identity checks,
remote termination, and tunnel removal.

The soak test accelerates discovery, disconnect, and removal cycles with a fake
SSH transport and checks retained heap and goroutine counts. It does **not**
replace a real deployment running for 48 hours with total memory, file descriptor,
child process, and responsiveness monitoring.

This is an initial Linux implementation. The full 48-hour soak and real-host
endurance run are release gates, not claims of durations already validated.
