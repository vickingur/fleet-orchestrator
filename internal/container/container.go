// Package container generates the podman scripts that build, run, list and
// remove agent containers on a host. It is pure; scripts run via the exec layer.
package container

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/vickingur/fleet-orchestrator/internal/inventory"
	"github.com/vickingur/fleet-orchestrator/internal/remote"
)

// Image is the local tag agent containers run.
const Image = "localhost/fleet-agent:latest"

// LabelAgent marks fleet-managed containers; its value is the agent name.
const LabelAgent = "fleet.agent"

// Secret maps one podman secret to the environment variable it becomes.
type Secret struct {
	Name   string
	EnvVar string
	Create string // command that creates it, shown when it is missing
}

var (
	// TailnetKey is mounted as a file (EnvVar unused), so podman exec sessions
	// do not inherit it. The agent user can still read it: prefer a one-off key.
	TailnetKey = Secret{"fleet-ts-authkey", "",
		"printf %s \"$TS_AUTHKEY\" | podman secret create fleet-ts-authkey -   # ephemeral, tagged tag:agent, ideally one-off"}
	// ClaudeToken carries a subscription token from `claude setup-token`, not an API key.
	ClaudeToken = Secret{"fleet-claude-token", "CLAUDE_CODE_OAUTH_TOKEN",
		"claude setup-token, then: printf %s \"$TOKEN\" | podman secret create fleet-claude-token -"}
)

// ProbeScript reports the image and which fleet secrets exist on the host.
func ProbeScript() string {
	return `if podman image exists ` + Image + `; then echo image=yes; else echo image=no; fi
podman secret ls --format '{{.Name}}' | sed 's/^/secret=/'
`
}

// Probe is what ProbeScript found.
type Probe struct {
	Image   bool
	Secrets map[string]bool
}

// ParseProbe reads ProbeScript output.
func ParseProbe(out string) (Probe, error) {
	p := Probe{Secrets: map[string]bool{}}
	sawImage := false
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		key, val, ok := strings.Cut(line, "=")
		switch {
		case !ok:
			return p, fmt.Errorf("unexpected probe line %q", line)
		case key == "image":
			p.Image, sawImage = val == "yes", true
		case key == "secret":
			p.Secrets[val] = true
		}
	}
	if !sawImage {
		return p, fmt.Errorf("probe output missing image line")
	}
	return p, nil
}

// Spec describes an agent container to start.
type Spec struct {
	Agent string
	// Tailnet joins the container to the tailnet as agent-<name>, tagged tag:agent.
	Tailnet bool
}

// Report explains what a spawn will wire up, so missing pieces are visible.
type Report struct {
	Args  []string
	Notes []string
}

// RunArgs builds the podman run arguments for spec given what the host has.
// A requested tailnet join without its key is an error, not a silent downgrade.
func RunArgs(spec Spec, probe Probe) (Report, error) {
	if !inventory.ValidAgentName(spec.Agent) {
		return Report{}, fmt.Errorf("invalid agent name %q", spec.Agent)
	}
	if !probe.Image {
		return Report{}, fmt.Errorf("image %s is missing on the host; build it with: fleet image <node>", Image)
	}
	name := remote.ContainerName(spec.Agent)
	args := []string{"run", "-d", "--init",
		"--name", name,
		"--hostname", "agent-" + spec.Agent,
		"--label", LabelAgent + "=" + spec.Agent,
		"--env", "FLEET_AGENT=" + spec.Agent,
		"--env", "FLEET_ACTOR=agent-" + spec.Agent,
		"--volume", name + "-work:/home/agent/work",
		// Agent homes persist so sessions and the Codex subscription login
		// survive restarts; the entrypoint re-applies the shared layer on start.
		"--volume", name + "-claude:/home/agent/.claude",
		"--volume", name + "-codex:/home/agent/.codex",
		// Fleet state and the audit log persist with the homes, so a respawn
		// knows what fleet wrote and agent actions stay reconstructable.
		"--volume", name + "-state:/home/agent/.local/state/fleet",
		"--pids-limit", "4096",
		"--security-opt", "no-new-privileges",
		"--cap-drop", "all",
	}
	var notes []string
	if spec.Tailnet {
		if !probe.Secrets[TailnetKey.Name] {
			return Report{}, fmt.Errorf("--tailnet needs podman secret %s on the host; create it with:\n  %s\nor spawn with --tailnet=false", TailnetKey.Name, TailnetKey.Create)
		}
		args = append(args, "--secret", TailnetKey.Name+",type=mount,target=ts-authkey,uid=1000,gid=1000,mode=0400")
		notes = append(notes, "tailnet: joins as agent-"+spec.Agent+" with tag:agent and Tailscale SSH")
	} else {
		notes = append(notes, "tailnet: off; reach it with fleet exec <node>:"+spec.Agent)
	}
	if probe.Secrets[ClaudeToken.Name] {
		args = append(args, "--secret", ClaudeToken.Name+",type=env,target="+ClaudeToken.EnvVar)
		notes = append(notes, "claude: subscription token from secret "+ClaudeToken.Name)
	} else {
		notes = append(notes, "claude: not signed in; create the secret once per host: "+ClaudeToken.Create)
	}
	notes = append(notes, "codex: sign in once with your ChatGPT subscription (kept in the codex volume): fleet ssh <node>:"+spec.Agent+" codex login --device-auth")
	args = append(args, Image)
	return Report{Args: args, Notes: notes}, nil
}

// BuildScript builds the agent image from the installed bundle on the host.
func BuildScript() string {
	return `set -eu
cd "$HOME/.local/share/fleet/bundle"
podman build --pull=newer -t ` + Image + ` -f container/Containerfile .
`
}

// RunScript executes podman with args, each single-quoted for sh.
func RunScript(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = shellQuote(a)
	}
	return "exec podman " + strings.Join(quoted, " ") + "\n"
}

// RemoveScript stops and deletes an agent container, and its work volume when asked.
func RemoveScript(agent string, volume bool) string {
	name := remote.ContainerName(agent)
	s := "set -eu\npodman rm -f -t 10 " + name + "\n"
	if volume {
		s += "podman volume rm -f " + name + "-work " + name + "-claude " + name + "-codex " + name + "-state\n"
	}
	return s
}

// ListScript prints fleet containers as JSON.
func ListScript() string {
	return "exec podman ps -a --filter label=" + LabelAgent + " --format json\n"
}

// Container is one agent container on a host.
type Container struct {
	Agent  string
	Name   string
	State  string
	Status string
	Image  string
}

// ParseList reads `podman ps --format json`.
func ParseList(out []byte) ([]Container, error) {
	var raw []struct {
		Names  []string          `json:"Names"`
		State  string            `json:"State"`
		Status string            `json:"Status"`
		Image  string            `json:"Image"`
		Labels map[string]string `json:"Labels"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parse podman ps: %w", err)
	}
	cs := make([]Container, 0, len(raw))
	for _, r := range raw {
		c := Container{Agent: r.Labels[LabelAgent], State: r.State, Status: r.Status, Image: r.Image}
		if len(r.Names) > 0 {
			c.Name = r.Names[0]
		}
		cs = append(cs, c)
	}
	slices.SortFunc(cs, func(a, b Container) int { return strings.Compare(a.Agent, b.Agent) })
	return cs, nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
