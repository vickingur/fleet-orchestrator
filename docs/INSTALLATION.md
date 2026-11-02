# Installation

## Develop without a live fleet

Clone this repository, install the Go version declared in `go.mod`, and run:

```sh
make build
./dist/fleet version
```

`make build` writes `dist/fleet`; it does not put anything on your PATH or update another machine. The executable remains named `fleet` even though the repository is named `fleet-orchestrator`. Build from source until versioned release artifacts are available.

For the full development gate, install `golangci-lint` v2.10.1, then run `make check`. The privacy target requires a Git checkout with staged files. A source archive can instead be checked with `python3 scripts/check-shareable.py`; use `--source-only` only when deliberately excluding generated `dist/` output.

## Set up your fleet config

Fleet deploys a fleet config that you keep in its own repository, apart from this source.

> **The fleet config is private.** Its inventory, guide and policy name your machines, accounts, SSH identities and tailnet addresses, and its shared layer is what your agents run with. Only ever push it to a private remote. Never commit it to this repository or a fork, and never attach it, a deployment bundle or a backup to a public issue or pull request.

Create one for a new fleet, with this machine as its controller:

```sh
make build
./dist/fleet init
```

It lands in `~/.local/share/fleet/config`, where fleet finds it from any directory, and holds an inventory naming this machine, the generated node guide, and a starter shared layer: agent instructions that point at the guide, settings that pre-approve only fleet's inert commands, and the `fleet` skill that teaches agents to operate the fleet. `--dir` or `FLEET_CONFIG` puts it elsewhere; fleet also uses the config it runs inside. To set up a further controller for an existing fleet, `fleet init <git-url>` clones its config instead. [`examples/config`](../examples/config) is a fuller fictional config.

1. Add your other machines to `fleet.toml`, using the configuration guide.
2. Add your own skills, settings and ensures under `home/`. Manifest checks and fixes are shell commands and must be trusted.
3. Run `fleet docs` to regenerate the node guide, then `fleet check`.
4. Commit, and push only to a private remote, for example one created with `gh repo create <name> --private --source . --push`. Check the visibility before the first push.
5. Use `./dist/fleet` until fleet is installed. `push` builds fleet for each node from this checkout; it finds it from the directory you run in, `FLEET_SRC`, or the path recorded by the last push.
6. Confirm ordinary OpenSSH access works under your account. Set `FLEET_SELF` to the inventory name of this local machine for operation without Tailscale. This identifies which commands execute locally; it must not name the remote destination. Configure credentials and host trust separately.
7. Review `fleet doctor <node>` and `fleet drift <node>` before choosing to deploy with `fleet push <node>`. Push this machine first: it installs `fleet` on your PATH. If you use Tailscale, `fleet ls` additionally shows tailnet status; it is not an SSH reachability check.

A push installs the bundle and binary and applies your configured shared layer. It may merge settings, replace managed files or run manifest fixes. Backups and an audit log are maintained in Fleet's state directory; read the generated guide for locations. This guide describes deployment steps; repository checks do not perform them.

## Containers

A member node with `runtime = "podman"` can build the container recipe and host named agent containers. `fleet spawn --tailnet=false <node> <agent>` skips tailnet enrollment and can be managed via `fleet exec <node>:<agent>` through the host. The image still installs Tailscale and configures its SSH proxy; direct outgoing Fleet connections from a container without a running Tailscale daemon are not supported by that default image configuration. Image builds download operating-system packages and agent tools. The current recipe uses upstream installer URLs and a latest Codex release; builds are not reproducible or pinned to a complete supply-chain lock.

Agent authentication is configured separately using the tools' supported subscription login flows. Do not put tokens in the inventory, source, image build arguments or committed environment files. The example tailnet policy is not installed automatically and must be reviewed against the actual network before use.
