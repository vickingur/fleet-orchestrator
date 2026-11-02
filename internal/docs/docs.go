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
}).Parse(`# Fleet

Generated from fleet.toml by ` + "`fleet docs`" + `. Do not edit by hand.

This is a personal fleet of machines on one Tailscale network. Every node runs
the same agent setup (Claude Code and Codex: instructions, settings, skills).
Use the ` + "`fleet`" + ` command for anything that touches another machine.

## Nodes

| Node | Tailnet host | OS | Role | Containers | SSH user | Notes |
|---|---|---|---|---|---|---|
{{- range .Nodes}}
| {{.Name}} | {{.Host}} | {{.OS}} | {{.Role}} | {{or_dash .Runtime}} | {{.User}} | {{cell .Notes}} |
{{- end}}

Agent containers are addressed as ` + "`node:agent`" + `, for example ` + "`worker:builder`" + `.

## Commands

| Command | What it does | Changes anything |
|---|---|---|
| ` + "`fleet ls`" + ` | Nodes joined with live tailnet status, plus agent containers on the tailnet | no |
| ` + "`fleet doctor [target]`" + ` | Reachability, agent tool versions, installed fleet version | no |
| ` + "`fleet drift [target]`" + ` | What a push would change on each node, without changing it | no |
| ` + "`fleet containers [node]`" + ` | Agent containers on a host | no |
| ` + "`fleet exec [--timeout 10m] [--detach] <target> -- <command>`" + ` | Run a shell command; ` + "`all`" + ` runs it everywhere in parallel. Stops it after ` + "`--timeout`" + ` (10m by default); ` + "`--detach`" + ` starts it in the background and prints its log path | yes |
| ` + "`fleet ssh <target> [command]`" + ` | Interactive session. A node's shell is its tmux session ` + "`fleet`" + `, which keeps running when the connection drops; the next ` + "`fleet ssh`" + ` resumes it | yes |
| ` + "`fleet push <target>`" + ` | Install the shared layer and fleet itself on a node (controller only) | yes |
| ` + "`fleet apply`" + ` | Apply the installed shared layer to this machine | yes |
| ` + "`fleet adopt <node> <path>`" + ` | Copy a locally edited shared file back into the repo (controller only) | repo only |
| ` + "`fleet restore [<set> [~/path ...]]`" + ` | List this machine's backup sets, or copy one back into place; the files it replaces are saved into a new set first | yes |
| ` + "`fleet image <node>`" + ` | Build the agent container image on a host | yes |
| ` + "`fleet spawn <node> <agent>`" + ` | Start an agent container | yes |
| ` + "`fleet rm <node> <agent>`" + ` | Remove an agent container | yes |
| ` + "`fleet codex <node>`" + ` | Attach this machine's Codex TUI to Codex running on a node | yes |

Add ` + "`--json`" + ` to ls, doctor, drift, containers and exec for machine-readable output.

## Rules for agents

1. Reach other machines only through ` + "`fleet`" + `. It pins host keys, refuses agent and
   port forwarding, enforces timeouts, runs commands under a POSIX shell whatever the
   login shell is, and records every action in ` + "`~/.local/state/fleet/audit.jsonl`" + `.
2. Do not use sudo on other machines. Nodes have no passwordless sudo; anything that
   needs root is for the owner to do.
3. Shared agent config is owned by the fleet repo on the controller. Never hand-edit
   a managed file on a node: the next push reports it as a conflict. Change the repo
   and run ` + "`fleet push all`" + ` instead.
4. Prefer an agent container for risky or long experiments. It has no access to the
   host's files, runs without capabilities, and can be thrown away with ` + "`fleet rm`" + `.
5. Check ` + "`fleet ls`" + ` before acting. An offline node is not an error to retry in a loop.
6. A job longer than a few minutes runs with ` + "`fleet exec --detach`" + `, then short ` + "`fleet exec`" + ` calls
   check on it; a foreground command is stopped after ` + "`--timeout`" + `. Files move through stdin
   and stdout: ` + "`fleet exec mac -- 'cat > ~/f' < f`" + ` and ` + "`fleet exec mac -- 'cat ~/f' > f`" + ` are byte-exact.

## Where things live on every node

| Path | Contents |
|---|---|
| ` + "`~/.local/share/fleet/bundle`" + ` | Installed shared layer and the fleet binary |
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
