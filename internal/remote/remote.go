// Package remote builds the exact commands fleet runs on nodes. It is pure:
// nothing here executes; callers hand the result to an execx.Runner.
package remote

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"time"

	"github.com/vickingur/fleet-orchestrator/internal/inventory"
)

// pathPreamble appends per-user and Homebrew bins. Non-login SSH shells on
// macOS start with only /usr/bin:/bin, which hides claude, codex and fleet.
// Appending, never prepending, keeps the node's own precedence (mise, nix and
// friends) so fleet runs the same binaries the owner's shell does.
const pathPreamble = `PATH="$PATH:$HOME/.local/bin:/opt/homebrew/bin:/usr/local/bin"; export PATH` + "\n"

// ShellCommand returns a one-line command that runs script under /bin/sh no
// matter which login shell parses it. The script travels base64-encoded inside
// single quotes holding no backslash or quote, so fish, zsh, bash and dash all
// read the line identically. Stdin stays free for the script's own use.
// If the payload cannot be decoded (no base64 -d on the node) the command
// exits 125 with a message instead of evaluating an empty string as success.
func ShellCommand(script string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(pathPreamble + script))
	return `sh -c 's=$(printf %s ` + encoded + ` | base64 -d) || { echo "fleet: this node cannot decode commands (base64 -d failed)" >&2; exit 125; }; eval "$s"'`
}

// DecodeFailure is the exit code ShellCommand uses when the node cannot decode it.
const DecodeFailure = 125

// ContainerName is the podman container name for an agent.
func ContainerName(agent string) string { return "fleet-agent-" + agent }

// InContainer wraps script so it runs inside the agent's container on the host.
func InContainer(agent, script string) string {
	return "exec podman exec -i " + ContainerName(agent) + " " + ShellCommand(script)
}

// SSHOptions are the hardening flags applied to every connection.
type SSHOptions struct {
	KnownHostsFile string
	ConnectTimeout time.Duration
	// ProxyCommand, when set, routes connections through it (for example
	// "tailscale nc %h %p" inside a userspace-networking container).
	ProxyCommand string
}

func (o SSHOptions) base(n inventory.Node) []string {
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=" + strconv.Itoa(int(o.ConnectTimeout.Seconds())),
		// Probe an idle link so a dead one is dropped within a minute and
		// a live one never looks idle to anything in between.
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=4",
		"-o", "ForwardAgent=no",
		"-o", "ForwardX11=no",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "UserKnownHostsFile=" + o.KnownHostsFile,
		"-o", "LogLevel=ERROR",
		"-p", strconv.Itoa(n.SSHPort()),
	}
	if o.ProxyCommand != "" {
		args = append(args, "-o", "ProxyCommand="+o.ProxyCommand)
	}
	if n.Identity != "" {
		args = append(args, "-i", n.Identity, "-o", "IdentitiesOnly=yes")
	}
	return args
}

// ExecArgs returns ssh arguments that run script on the node non-interactively.
// Port forwarding is cleared so a command can never open tunnels.
func (o SSHOptions) ExecArgs(n inventory.Node, script string) []string {
	args := append(o.base(n), "-T", "-o", "ClearAllForwardings=yes")
	return append(args, "-l", n.User, "--", n.Host, ShellCommand(script))
}

// PersistentSession attaches to the node's "fleet" tmux session, creating it
// on first use. The session outlives the connection: a dropped link, a closed
// window or a sleeping laptop leaves it running, and the next attach resumes it.
const PersistentSession = `exec tmux new-session -A -s fleet \; set-option mouse on`

// InteractiveArgs returns ssh arguments for an interactive session that runs
// script on the node.
func (o SSHOptions) InteractiveArgs(n inventory.Node, script string) []string {
	args := append(o.base(n), "-t", "-o", "ClearAllForwardings=yes")
	return append(args, "-l", n.User, "--", n.Host, ShellCommand(script))
}

// TunnelArgs returns ssh arguments that forward localPort on this machine to
// remotePort on the node's loopback while script runs there.
func (o SSHOptions) TunnelArgs(n inventory.Node, localPort, remotePort int, script string) []string {
	forward := fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", localPort, remotePort)
	args := append(o.base(n), "-T", "-o", "ExitOnForwardFailure=yes", "-L", forward)
	return append(args, "-l", n.User, "--", n.Host, ShellCommand(script))
}
