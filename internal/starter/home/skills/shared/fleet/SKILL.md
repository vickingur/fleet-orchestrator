---
name: fleet
description: Operates this fleet of machines and agent containers with the `fleet` CLI: check what is online, run commands on another machine, compare or push the shared agent setup, start or remove sandbox agent containers, attach Codex to another node. Use whenever a task touches a machine other than this one, names another node or the fleet, needs an isolated container to run something risky, or asks to change the agent setup (instructions, settings, skills, plugins) on every machine.
---

# Fleet

`fleet` is the only sanctioned way to reach another machine. It records first-use host keys and rejects changed ones, refuses agent forwarding, runs commands under POSIX `sh` whatever the login shell is, enforces timeouts, and writes every action to `~/.local/state/fleet/audit.jsonl`. Never use raw `ssh`, `scp` or `tailscale ssh` for fleet work.

The nodes, their users and runtimes, and where fleet keeps things are in `~/.config/fleet/FLEET.md`. Read it for names; do not guess them. The examples below use `<node>`.

## Look before acting

```sh
fleet ls --json          # nodes, tailnet state, agent containers on the tailnet
fleet doctor --json      # reachability, fleet version, agent tool versions
```

An offline node is a fact to report, not an error to retry. `fleet ls`, `fleet containers` and `fleet version` run without approval; everything else asks. `doctor` runs the version commands in the inventory, so it is not inert.

## Run something elsewhere

```sh
fleet exec <node> -- 'uname -sm'
fleet exec all -- 'claude --version'                # every node in parallel
fleet exec --json <node>,<node> -- 'df -h ~'         # comma list; JSON per target
fleet exec <node>:<agent> -- 'id'                   # inside an agent container
fleet exec --detach <node> -- 'make test'           # long job; prints its log path
```

Quote the command as one argument. The remote exit code becomes fleet's exit code. Files move byte-exact through stdin and stdout: `fleet exec <node> -- 'cat > ~/f' < f`. Do not assume passwordless sudo: anything needing root goes back to the user with the exact command.

## Change the shared agent setup

The shared setup lives in the fleet config on the controller (`~/.local/share/fleet/config` unless `FLEET_CONFIG` names another), under `home/`, mapped by `home/manifest.toml`. Never hand-edit a managed file on a node; the next push reports it as a conflict.

1. Edit the config: `home/instructions.md`, the settings overlays, `home/skills/`.
2. `fleet check` validates it; run any tests the config ships.
3. `fleet drift` previews every node, then `fleet push all`.

A plugin enabled in the Claude settings also needs an `[[ensure]]` that installs it; `fleet check` enforces that. When a file edited on a node should win: `fleet adopt <node> '~/path'`, review the diff in the config, commit, push.

Exit codes: 0 ok, 1 error, 2 usage, 3 conflicts left untouched, 4 drift found.

## Sandbox agent containers

Use one for risky or long experiments, on a node whose runtime is podman. It runs as a non-root user without capabilities and cannot see the host's files.

```sh
fleet spawn --tailnet=false <node> scratch
fleet exec <node>:scratch -- 'git clone … && make test'
fleet containers
fleet rm <node> scratch            # keeps volumes; --volumes deletes them
```

Agents sign in with the user's subscriptions, never API keys. Claude reads its token from the `fleet-claude-token` podman secret; Codex needs a one-time interactive login, `fleet ssh <node>:scratch codex login --device-auth`. Hand that step to the user.

## Interactive and Codex

`fleet ssh <target>` opens the node's persistent tmux session `fleet`, which survives disconnects. `fleet codex <node>` attaches this machine's Codex TUI to Codex on that node through a token-protected tunnel; `fleet codex --check <node>` verifies it without opening the TUI. Both are interactive: suggest them to the user rather than running them yourself. The user's own view of everything is `fleet` with no arguments, a terminal UI.
