# Configuration and examples

Fleet keeps the tool and what it deploys apart. A fleet config is a directory with `fleet.toml` (the inventory), its generated `FLEET.md`, the `home/` shared layer mapped by `home/manifest.toml`, and optionally a tailnet policy in `policy/tailnet.hujson`. Keep yours in its own Git repository with a private remote, never in this repository or a fork: it maps your machines, accounts and network. [`examples/config`](../examples/config) is the fictional example the tests use.

Fleet finds the config through `FLEET_CONFIG`, else the config it runs inside, else `~/.local/share/fleet/config`. A machine with none, such as every node but the controller, reads the inventory of its installed bundle. `push` also needs this source checkout to build fleet for each node: `FLEET_SRC`, else the checkout it runs inside, else the one the installed bundle was pushed from.

## Inventory

```toml
[[node]]
name = "laptop"
host = "laptop-example"
user = "operator"
os = "darwin"
role = "controller"

[[node]]
name = "worker"
host = "worker-example"
user = "operator"
os = "linux"
role = "member"
runtime = "podman"

[[tool]]
name = "codex"
version = "codex --version"
```

These values are fictional. `name` is Fleet's target alias. `host` can be a reachable DNS name, OpenSSH host alias or IPv4 address; IPv6 literals are not currently accepted. Fleet supplies `user` and `port` explicitly to SSH, so inventory values take precedence over alias defaults. For optional Tailscale status and automatic self-identification, use the short MagicDNS host label. The `.invalid` addresses in committed fixtures intentionally do not identify real nodes.

There must be exactly one controller. Supported `os` values are `darwin` and `linux`; roles are `controller` and `member`. `runtime` is absent or `podman`. Optional `port` defaults to SSH port 22; optional `identity` names a private, local identity path. Never commit the key itself. Unknown TOML keys and invalid names fail validation. A tool's `version` is a shell command executed by `doctor`, not descriptive text.

After editing the inventory, run `fleet docs` to regenerate `FLEET.md`, then `fleet check`. The guide names your machines; keep it private with the rest of the config.

## Transport support and limits

| Feature | Ordinary SSH without Tailscale |
|---|---|
| `exec`, `doctor`, `drift`, `push`, `adopt` | Supported with an explicit local `FLEET_SELF`; exercised in integration tests |
| `ssh`, `codex`, host/container administration | Use the same SSH transport and local-node identification; require the target tools/runtime appropriate to the command |
| Local `docs`, `version`, `apply`, `restore` | Do not need tailnet discovery |
| `ls` | Lists inventory but still attempts Tailscale status; an unavailable daemon/CLI yields unknown state, not an SSH failure |
| Terminal UI | Status remains tailnet-based; hosts absent from an active tailnet can have actions disabled even if SSH works. Use the CLI for those hosts |
| `spawn --tailnet=false` | Skips joining the container to a tailnet; manage it through the host with `node:agent` |
| Outgoing Fleet SSH from the bundled agent image | The default proxy invokes Tailscale even when enrollment is skipped; not a transport-neutral image |

For ordinary SSH, set `FLEET_SELF` to this machine's configured `name` before invoking operational commands. That node runs commands locally, so never use the remote target's name here. Without an override, operational commands query Tailscale and fail if its status is unavailable. See [the tested example](../examples/ssh/README.md).

## Shared layer

The `home/manifest.toml` maps source files to home-directory targets:

```toml
[[file]]
source = "instructions.md"
target = "~/.claude/CLAUDE.md"
merge = "block"

[[tree]]
source = "skills/shared"
target = "~/.claude/skills"
```

File merge strategies are `replace`, `block`, `json` and `toml`. A tree copies regular files and excludes machine-state directories. Targets must be under `~/`. Fleet tracks prior writes and reports conflicts when local managed files have changed.

An optional `[[ensure]]` contains `name`, `check` and `fix` shell commands; `snapshot` lists additional files that a fix changes so they can be backed up. Even a drift preview runs ensure checks. The example layer deliberately has no ensures, plugin installation actions, credentials or command approvals.

## Checking a config

`fleet check [dir]` validates a config before it is pushed and exits 1 with one line per problem (`--json` for machine-readable output):

| Check | Requires |
|---|---|
| `inventory`, `manifest` | Both files parse; every manifest source exists under `home/` |
| `guide` | `FLEET.md` matches what the inventory renders |
| `layer` | No nested `.git`, `.plans`, `.sessions`, archives or symlinks under `home/` |
| `pre-approval` | The Claude settings and Codex rules the manifest ships pre-approve only `fleet ls`, `containers` and `version`; `doctor` and `drift` run config-defined commands |
| `plugins` | Every plugin enabled in the shipped Claude settings has an ensure that installs it, after one that adds its marketplace |
| `terminfo` | Every shipped `*.terminfo` compiles with `tic -x` |
| `policy` | If present, agents reach only agents, root SSH needs `check`, a policy test denies `tag:agent`, and `tag:agent` has an owner |

Run tests your shared layer ships, such as a vendored hook's, from the config's own repository.

## Typical private operations

```sh
fleet ls --json
fleet doctor worker
fleet drift worker
fleet exec worker -- 'uname -sm'
```

After reviewing the proposed changes, `fleet push worker` deploys to that target. `fleet restore` lists backup sets; restoring a selected set is a separate write operation. `fleet adopt <node> <path>` copies a locally edited managed file into the fleet config for review.

Exit codes are 0 for success, 1 for an error, 2 for usage errors, 3 for unresolved conflicts, and 4 for detected drift. `ls`, `doctor`, `drift`, `containers` and `exec` support JSON output.

## Runtime controls

- `FLEET_CONFIG`: the fleet config: inventory, guide and shared layer.
- `FLEET_SRC`: the source checkout `push` builds fleet from.
- `FLEET_SELF`: this machine's configured node name, required for operational CLI commands without Tailscale self-discovery. Commands aimed at that name execute locally; it is not a remote target selector.
- `FLEET_TAILSCALE_SOCKET`: selects the Tailscale daemon socket.
- `FLEET_SSH_PROXY`: trusted SSH proxy command, used by userspace-network containers.

These values can affect which machines or commands Fleet operates on. Keep fleet config values out of this source, CI and issue reports. SSH host keys, state, backups and audits are private runtime data.

## Security boundaries

Fleet executes trusted commands and synchronizes configuration under the user's account. Inventory tool commands, manifest checks/fixes, SSH proxy configuration and managed instructions are code or operational input. Do not run a checkout or bundle you do not trust.

`FLEET_SELF` identifies the machine running Fleet. Commands targeting that inventory name execute locally. Verify it before operating; do not set it to a remote destination merely to avoid discovery. DNS/IPv4 SSH access does not require Tailscale, but the discovery UI and default container proxy retain Tailscale-specific behavior.

- SSH uses a Fleet-owned known-hosts file with first-use acceptance (`accept-new`). Changed keys are rejected, but first contact still requires trust in the network and target identity.
- Ordinary SSH operations disable agent/X11 forwarding and clear configured port forwards. The Codex attachment command intentionally opens a dedicated local tunnel and uses a token handshake.
- Agent containers use a restricted runtime configuration. For containers joined to Tailscale, tailnet reachability also depends on the deployed policy. Containers spawned without tailnet enrollment still have their ordinary container networking. The example policy is not automatically installed and is not evidence of the state of any live network.
- Drift can run configured ensure checks. A dry-run label does not make arbitrary manifest commands safe.
- A fleet config is private: its inventory, generated guide and policy map machines, accounts, SSH identities and network addresses, and its shared layer is code agents run. Keep it in a repository with a private remote, never in this repository or a fork. Generated deployment bundles include the inventory and the controller source path. Backups, registry, credentials and audit logs are private data too.
- Container recipes fetch upstream installers and a latest tool release. A reproducible, version-pinned container supply chain remains release work.

If a credential is ever committed to a fleet config, deleting it in a later commit is not enough: it stays in Git history and any clone. Replace the credential.
