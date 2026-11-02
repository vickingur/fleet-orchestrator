# Fleet Orchestrator

A Go CLI and terminal UI for operating machines over Tailscale, synchronizing agent configuration, and managing agent containers. Fleet provides SSH commands, configuration drift checks, conflict-aware updates, backups and restoration, and JSON output for automation.

This repository is being prepared for public release. It contains reusable source and fictional examples. No project license has been selected yet; see [provenance and licensing](docs/PROVENANCE.md).

## Build and check

Prerequisites: Git, Make, Python 3.9+, the Go version in `go.mod`, and `golangci-lint` v2.10.1 for the full check. Runtime operation additionally requires Tailscale and OpenSSH. Podman is needed only on nodes that host agent containers; tmux supports persistent interactive sessions.

```sh
git clone https://github.com/vickingur/fleet-orchestrator.git
cd fleet-orchestrator
make example
make build
./dist/fleet version
make check
```

Repository access requires authorization while it is private. `make example` creates an ignored `fleet.toml` from the fictional inventory and generates the ignored node guide, `FLEET.md`. It refuses to replace an inventory that has been customized. These steps build and test locally; they do not install Fleet or contact configured nodes.

The test gate runs formatting, vet, lint, race-enabled Go tests, privacy-checker regression tests, and a scan of staged Git blobs. CI checks Linux and macOS and runs a separate Docker SSH integration test. Run `make e2e` locally with Docker available; the test creates an isolated controller home, temporary keys, and a throwaway SSH container. It skips without Docker locally; CI explicitly requires Docker first.

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
| `fleet restore` | List local backup sets |

The names in this table are fictional. `doctor` and `drift` can execute commands defined in the inventory or manifest; review configuration before running them. `push`, `apply`, `adopt`, restore with arguments, and container operations can change files or machines. See [configuration](docs/CONFIGURATION.md) and [security boundaries](SECURITY.md).

## Keep deployment settings separate

Use a separate private deployment checkout for real node names, accounts, SSH identity paths and the `home/` layer. The development checkout should retain only fictional fixtures. The [installation guide](docs/INSTALLATION.md) explains the distinction.

| Reusable repository content | Private deployment content |
|---|---|
| CLI, packages, tests, container recipes and checks | Real inventory and generated node guide |
| `fleet.example.toml` and minimal example `home/` | Personal instructions, plugins, skills and configuration |
| Example policy with documentation addresses | Actual policy, topology and tailnet information |
| Documentation and dependency notices | Credentials, SSH material, registry, backups and logs |

Fleet deployment bundles include inventory and the controller source path. Keep them private. The source repository does not include the original deployment's history.

Before committing, stage the intended changes and run `make check`. `make privacy` checks the staged content, so a clean working copy cannot hide a staged secret. Ignore rules are only a convenience; the checker also rejects tracked deployment inventory and generated guides. Pattern scans cannot detect every secret or personal detail: review the diff.

- [Installation](docs/INSTALLATION.md)
- [Configuration and examples](docs/CONFIGURATION.md)
- [Contributing](CONTRIBUTING.md)
- [Security](SECURITY.md)
- [Provenance and licensing](docs/PROVENANCE.md)
- [Public-release checklist](docs/RELEASE_READINESS.md)
