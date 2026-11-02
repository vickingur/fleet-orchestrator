# Installation

## Develop without a live fleet

Clone this repository, install the Go version declared in `go.mod`, and run:

```sh
make example
make build
./dist/fleet version
```

`make build` writes `dist/fleet`; it does not put anything on your PATH or update another machine. The executable remains named `fleet` even though the repository is named `fleet-orchestrator`. Build from source until versioned release artifacts are available.

For the full development gate, install `golangci-lint` v2.10.1, then run `make check`. The privacy target requires a Git checkout with staged files. A source archive can instead be checked with `python3 scripts/check-shareable.py`; use `--source-only` only when deliberately excluding generated `dist/` output.

## Prepare a private deployment checkout

Use a different checkout for actual operations. Keep its real `fleet.toml`, `FLEET.md` and customized `home/` private. Both inventory and generated guide are ignored, but files under `home/` are tracked: do not push deployment customization to the reusable repository.

1. Copy `fleet.example.toml` to `fleet.toml` in that private checkout.
2. Replace the fictional node fields with your own, using the configuration guide.
3. Review each manifest entry and remove example skills you do not need. Manifest checks and fixes are shell commands and must be trusted.
4. Run `go run ./cmd/fleet docs` to generate the private node guide.
5. Run `make build`, then use the binary from this checkout. `FLEET_SRC` can point to the private checkout when invoking the binary from another directory.
6. Confirm Tailscale is connected and normal OpenSSH access works under your account. Configure credentials and host trust separately. Fleet cannot provision those prerequisites for you.
7. Review `fleet ls`, then `fleet doctor <node>` and `fleet drift <node>` before choosing to deploy with `fleet push <node>`.

A push installs the bundle and binary and applies your configured shared layer. It may merge settings, replace managed files or run manifest fixes. Backups and an audit log are maintained in Fleet's state directory; read the generated guide for locations. This guide describes deployment steps; repository checks do not perform them.

## Containers

A member node with `runtime = "podman"` can build the container recipe and host named agent containers. Image builds download operating-system packages and agent tools. The current recipe uses upstream installer URLs and a latest Codex release; builds are not reproducible or pinned to a complete supply-chain lock.

Agent authentication is configured separately using the tools' supported subscription login flows. Do not put tokens in the inventory, source, image build arguments or committed environment files. The example tailnet policy is not installed automatically and must be reviewed against the actual network before use.
