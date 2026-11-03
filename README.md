# Fleet Orchestrator

A Go CLI for managing machines over SSH, synchronizing agent configuration, and managing agent containers, with a terminal UI and optional Tailscale discovery. Fleet provides command execution, configuration drift checks, conflict-aware updates, backups and restoration, and JSON output for automation.

This repository contains the tool and fictional examples. Fleet is licensed under [MIT](LICENSE); see [provenance and licensing](docs/PROVENANCE.md).

## Build and check

Prerequisites: Git, Make, Python 3.9+, the Go version in `go.mod`, and `golangci-lint` v2.10.1 for the full check. Remote operations use OpenSSH; Tailscale is optional for the core CLI when the local node is identified explicitly with `FLEET_SELF`. Podman is needed only on nodes that host agent containers; tmux supports persistent interactive sessions.

```sh
git clone https://github.com/vickingur/fleet-orchestrator.git
cd fleet-orchestrator
make build
./dist/fleet version
make check
```

These steps build and test locally against the fictional config in `examples/config`; they do not install Fleet or contact configured nodes.

The test gate runs formatting, vet, lint, race-enabled Go tests, privacy-checker regression tests, and a scan of staged Git blobs. CI checks Linux and macOS and runs a separate Docker SSH integration test. Run `make e2e` locally with Docker available; the test creates an isolated controller home, temporary keys, and a throwaway SSH container. It skips without Docker locally; CI explicitly requires Docker first.

## Ordinary SSH and optional Tailscale

Core CLI operations work with reachable DNS names or IPv4 addresses over a LAN, another VPN, or an SSH-accessible network. Set `FLEET_SELF` to the inventory name of the machine running Fleet; a target with that name executes locally. Do not set it to the remote target. Without this setting, Fleet uses Tailscale to identify the local node.

See the [tested ordinary SSH example](examples/ssh/README.md). CI runs its configuration against a disposable SSH server with Tailscale deliberately unavailable, exercising execution, drift, push, doctor and adoption.

Tailscale is still used for discovery/status: `fleet ls` reports unknown status when it is unavailable, and the terminal UI can disable actions for hosts absent from an active tailnet. Use the CLI for ordinary SSH hosts. The bundled agent image also contains Tailscale-specific networking; see the [transport support table](docs/CONFIGURATION.md#transport-support-and-limits) before using it without a tailnet.

## What it does

| Command | Purpose |
|---|---|
| `fleet` | Open the terminal UI |
| `fleet ls --json` | List configured nodes with current tailnet state |
| `fleet doctor worker` | Check reachability and configured tool versions |
| `fleet drift worker` | Preview file changes and run manifest ensure checks |
| `fleet exec worker -- 'uname -sm'` | Run a command under a POSIX shell |
| `fleet ssh worker` | Resume an interactive tmux session |
| `fleet push worker` | Deploy the configured shared layer and CLI |
| `fleet init` | Create a fleet config with this machine as the controller |
| `fleet check` | Validate a fleet config before pushing it |
| `fleet restore` | List local backup sets |

The names in this table are fictional. `doctor` and `drift` can execute commands defined in the inventory or manifest; review configuration before running them. `push`, `apply`, `adopt`, restore with arguments, and container operations can change files or machines. See [configuration](docs/CONFIGURATION.md) and its [security boundaries](docs/CONFIGURATION.md#security-boundaries).

## The tool and your fleet config

This repository is the tool. What it deploys lives in a fleet config: your inventory, generated node guide, `home/` shared layer and optional tailnet policy, kept in its own Git repository.

> **Keep your fleet config private.** It maps your machines: host names, accounts, SSH identity paths, tailnet addresses and access policy, plus the instructions and settings your agents run with. Push it only to a private remote. Never commit it to this repository or a fork of it, and never paste its contents into a public issue or pull request.
 Fleet looks for it through `FLEET_CONFIG`, the directory it runs in, or `~/.local/share/fleet/config`, where `fleet init` creates one, or clones yours on a further controller. The [installation guide](docs/INSTALLATION.md) walks through it; [`examples/config`](examples/config) is a fictional one.

| This repository | Your fleet config |
|---|---|
| CLI, packages, tests, container recipes and checks | Real inventory and generated node guide |
| The fictional `examples/config` | Personal instructions, plugins, skills and configuration |
| Example policy with documentation addresses | Actual policy, topology and tailnet information |
| Documentation and dependency notices | |

Credentials, SSH material, the registry, backups and logs belong in neither. Fleet deployment bundles include the inventory and the controller's source path; keep them private. The source repository does not include the original deployment's history.

Before committing, stage the intended changes and run `make check`. `make privacy` checks the staged content, so a clean working copy cannot hide a staged secret. Ignore rules are only a convenience; the checker also rejects a tracked inventory or guide at the root. Pattern scans cannot detect every secret or personal detail: review the diff.

- [Installation](docs/INSTALLATION.md)
- [Configuration and examples](docs/CONFIGURATION.md)
- [Contributing](CONTRIBUTING.md)
- [Security boundaries](docs/CONFIGURATION.md#security-boundaries)
- [Provenance and licensing](docs/PROVENANCE.md)
- [Public-release checklist](docs/RELEASE_READINESS.md)
