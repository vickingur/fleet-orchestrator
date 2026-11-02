# Configuration and examples

`fleet.example.toml` is the shareable fixture. The runtime reads `fleet.toml` from the source checkout or installed bundle. Keep real inventory in a separate private deployment checkout.

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

These values are fictional. `name` is Fleet's target alias. Use the short MagicDNS host label in a real `host` value so tailnet state and automatic self-identification can match it; the `.invalid` addresses in the committed fixture intentionally do not identify real nodes.

There must be exactly one controller. Supported `os` values are `darwin` and `linux`; roles are `controller` and `member`. `runtime` is absent or `podman`. Optional `port` defaults to SSH port 22; optional `identity` names a private, local identity path. Never commit the key itself. Unknown TOML keys and invalid names fail validation. A tool's `version` is a shell command executed by `doctor`, not descriptive text.

After editing inventory in the private deployment checkout, run `fleet docs`. Do not publish the resulting `FLEET.md`.

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

## Typical private operations

```sh
fleet ls --json
fleet doctor worker
fleet drift worker
fleet exec worker -- 'uname -sm'
```

After reviewing the proposed changes, `fleet push worker` deploys to that target. `fleet restore` lists backup sets; restoring a selected set is a separate write operation. `fleet adopt <node> <path>` copies a locally edited managed file into the controller source for review.

Exit codes are 0 for success, 1 for an error, 2 for usage errors, 3 for unresolved conflicts, and 4 for detected drift. `ls`, `doctor`, `drift`, `containers` and `exec` support JSON output.

## Runtime controls

- `FLEET_SRC`: source checkout for inventory and the managed layer.
- `FLEET_SELF`: explicitly selects this machine's configured node name when automatic identification is unsuitable.
- `FLEET_TAILSCALE_SOCKET`: selects the Tailscale daemon socket.
- `FLEET_SSH_PROXY`: trusted SSH proxy command, used by userspace-network containers.

These values can affect which machines or commands Fleet operates on. Keep deployment values out of source, CI and issue reports. SSH host keys, state, backups and audits are private runtime data.
