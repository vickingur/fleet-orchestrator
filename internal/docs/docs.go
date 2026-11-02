// Package docs renders FLEET.md, the contract agents read before operating
// any machine. It is generated from the inventory so it cannot drift from it.
package docs

import (
	"bytes"
	"strings"
	"text/template"

	"github.com/vickingur/fleet-orchestrator/internal/inventory"
)

var tmpl = template.Must(template.New("fleet").Funcs(template.FuncMap{
	"or_dash": func(s string) string {
		if s == "" {
			return "-"
		}
		return s
	},
	"cell": func(s string) string { return strings.ReplaceAll(s, "|", `\|`) },
	// containerHost names the first node that hosts agent containers, if any.
	"containerHost": func(nodes []inventory.Node) string {
		for _, n := range nodes {
			if n.Runtime != "" {
				return n.Name
			}
		}
		return ""
	},
}).Parse(`# Fleet

Generated from fleet.toml by ` + "`fleet docs`" + `. Do not edit by hand.

Fleet manages machines over SSH and can synchronize agent instructions, settings
and skills. Tailscale discovery is optional for core CLI operations.
Use the ` + "`fleet`" + ` command for anything that touches another machine.

## Nodes

| Node | SSH host | OS | Role | Containers | SSH user | Notes |
|---|---|---|---|---|---|---|
{{- range .Nodes}}
| {{.Name}} | {{.Host}} | {{.OS}} | {{.Role}} | {{or_dash .Runtime}} | {{.User}} | {{cell .Notes}} |
{{- end}}
{{- with containerHost .Nodes}}

Agent containers are addressed as ` + "`node:agent`" + `, for example ` + "`{{.}}:builder`" + `.
{{- end}}

## Transport

For ordinary DNS/IPv4 SSH access, set ` + "`FLEET_SELF`" + ` to this local machine's inventory name.
A target with that name executes locally; never set it to a remote destination.
Without this override, operational commands use Tailscale to identify this machine.
` + "`fleet ls`" + ` and terminal UI status remain tailnet-based; unknown or absent tailnet
status does not prove an SSH host is unreachable. Use the CLI for generic SSH hosts.
Containers may skip enrollment with ` + "`--tailnet=false`" + `, but the bundled image's
outgoing SSH proxy still uses Tailscale.

## Commands

| Command | What it does | Changes anything |
|---|---|---|
| ` + "`fleet ls`" + ` | Nodes joined with live tailnet status, plus agent containers on the tailnet | no |
| ` + "`fleet doctor [target]`" + ` | Reachability, agent tool versions, installed fleet version | runs configured commands |
| ` + "`fleet drift [target]`" + ` | Preview file changes and run manifest ensure checks | runs configured checks |
| ` + "`fleet containers [node]`" + ` | Agent containers on a host | no |
| ` + "`fleet exec [--timeout 10m] [--detach] <target> -- <command>`" + ` | Run a shell command; ` + "`all`" + ` runs it everywhere in parallel. Stops it after ` + "`--timeout`" + ` (10m by default); ` + "`--detach`" + ` starts it in the background and prints its log path | yes |
| ` + "`fleet ssh <target> [command]`" + ` | Interactive session. A node's shell is its tmux session ` + "`fleet`" + `, which keeps running when the connection drops; the next ` + "`fleet ssh`" + ` resumes it | yes |
| ` + "`fleet push <target>`" + ` | Install the shared layer and fleet itself on a node (controller only) | yes |
| ` + "`fleet apply`" + ` | Apply the installed shared layer to this machine | yes |
| ` + "`fleet adopt <node> <path>`" + ` | Copy a locally edited shared file back into the fleet config (controller only) | config only |
| ` + "`fleet check [dir]`" + ` | Validate a fleet config before pushing it | no |
| ` + "`fleet restore [<set> [~/path ...]]`" + ` | List this machine's backup sets, or copy one back into place; the files it replaces are saved into a new set first | yes |
| ` + "`fleet image <node>`" + ` | Build the agent container image on a host | yes |
| ` + "`fleet spawn <node> <agent>`" + ` | Start an agent container | yes |
| ` + "`fleet rm <node> <agent>`" + ` | Remove an agent container | yes |
| ` + "`fleet codex <node>`" + ` | Attach this machine's Codex TUI to Codex running on a node | yes |

Add ` + "`--json`" + ` to ls, doctor, drift, containers and exec for machine-readable output.

## Rules for agents

1. Reach other machines through ` + "`fleet`" + `. It records first-use SSH host keys, rejects
   changed keys, disables agent forwarding, enforces timeouts, and records actions in
   ` + "`~/.local/state/fleet/audit.jsonl`" + `. Ordinary commands clear port forwards; Codex
   attachment intentionally creates a token-authenticated local tunnel.
2. Do not use sudo on other machines without the owner's authorization. Do not assume
   passwordless sudo or administrative privileges.
3. Shared agent config is owned by the fleet config on the controller. Never hand-edit
   a managed file on a node: the next push reports it as a conflict. Change the config,
   run ` + "`fleet check`" + `, then ` + "`fleet push all`" + `.
4. Prefer an agent container for risky or long experiments. It has no access to the
   host's files, runs without capabilities, and can be thrown away with ` + "`fleet rm`" + `.
5. For tailnet status use ` + "`fleet ls`" + `; for SSH reachability use ` + "`fleet doctor <node>`" + `.
   Doctor executes configured version commands, so review them first. Do not retry
   unreachable nodes in a loop.
6. A job longer than a few minutes runs with ` + "`fleet exec --detach`" + `, then short ` + "`fleet exec`" + ` calls
   check on it; a foreground command is stopped after ` + "`--timeout`" + `. Files move through stdin
   and stdout: ` + "`fleet exec mac -- 'cat > ~/f' < f`" + ` and ` + "`fleet exec mac -- 'cat ~/f' > f`" + ` are byte-exact.

## Where things live on every node

| Path | Contents |
|---|---|
| ` + "`~/.local/share/fleet/bundle`" + ` | Installed shared layer and the fleet binary |
| ` + "`~/.local/share/fleet/config`" + ` | The fleet config, on the controller only, unless ` + "`FLEET_CONFIG`" + ` names another |
| ` + "`~/.local/bin/fleet`" + ` | The fleet command |
| ` + "`~/.local/state/fleet/state.json`" + ` | What fleet last wrote, used to detect local edits |
| ` + "`~/.local/state/fleet/backup/`" + ` | A copy of every file before fleet changed it |
| ` + "`~/.local/state/fleet/audit.jsonl`" + ` | Every remote action taken from this node |
`))

// Render produces FLEET.md for the inventory.
func Render(inv *inventory.Inventory) []byte {
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, inv); err != nil {
		panic(err) // the template is static; failure is a programming error
	}
	return buf.Bytes()
}
