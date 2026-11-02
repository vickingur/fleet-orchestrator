package tui

import (
	"fmt"
	"sort"
	"strings"
)

// Agents fixes can be delegated to, in toggle order.
var agents = []string{"claude", "codex"}

var agentNames = map[string]string{"claude": "Claude", "codex": "Codex"}

// problems lists what the TUI can see wrong with a node, as plain sentences an
// agent can act on. It reads only state already loaded; nothing is fetched.
func (m Model) problems(nodeName string) []string {
	n := m.node(nodeName)
	var out []string
	if !reachable(n) {
		return []string{"it is not on the tailnet (state: " + n.State + ")"}
	}
	if strings.HasPrefix(n.State, "offline") {
		out = append(out, "it is offline on the tailnet ("+n.State+")")
	}
	if e := m.healthErr[n.Name]; e != "" {
		out = append(out, "checking it failed: "+e)
	}
	if h, ok := m.health[n.Name]; ok {
		switch {
		case h.Reach != "ok":
			out = append(out, "it is unreachable: "+h.Error)
		case h.Fleet == "missing":
			out = append(out, "fleet is not installed on it")
		case !h.Current:
			out = append(out, "its fleet install ("+h.Fleet+") is older than the repo's")
		}
		names := make([]string, 0, len(h.Tools))
		for name := range h.Tools {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if h.Tools[name] == "missing" {
				out = append(out, name+" is not installed or not on PATH")
			}
		}
	}
	if r, ok := m.plans[n.Name]; ok {
		if r.Error != "" {
			out = append(out, "previewing a push failed: "+r.Error)
		}
		if r.Plan != nil {
			for _, c := range r.Plan.Changes {
				switch c.Action {
				case "conflict":
					out = append(out, fmt.Sprintf("conflict on %s (%s)", c.Target, c.Detail))
				case "ok":
				default:
					out = append(out, fmt.Sprintf("a push would %s %s", c.Action, c.Target))
				}
			}
			for _, e := range r.Plan.Ensures {
				switch {
				case e.Error != "":
					out = append(out, fmt.Sprintf("ensure %q fails: %s", e.Name, e.Error))
				case !e.Satisfied:
					out = append(out, fmt.Sprintf("ensure %q is not met", e.Name))
				}
			}
		}
	}
	for _, c := range m.containers {
		if c.Node == n.Name && c.Agent != "" && c.State != "running" {
			out = append(out, fmt.Sprintf("agent container %s is %s (%s)", c.Agent, c.State, c.Status))
		}
	}
	return out
}

// delegatePrompt is the opening message for the agent: the task, the rules it
// must follow, and everything the TUI knows, so it starts from evidence.
func (m Model) delegatePrompt(target, note string, problems []string, controller bool) string {
	n := m.node(strings.SplitN(target, ":", 2)[0])
	var b strings.Builder
	fmt.Fprintf(&b, "Fix fleet problems on %s.\n\n", target)
	b.WriteString("Use the fleet skill: reach other machines only through the fleet CLI and read ~/.config/fleet/FLEET.md for the nodes. ")
	if controller {
		b.WriteString("You are in the fleet repo on the controller: shared config changes go into home/ here, then make check and fleet push; never hand-edit managed files on a node. ")
	} else {
		b.WriteString("This machine is not the controller: shared config changes must be made in the fleet repo on the MacBook. ")
	}
	b.WriteString("Ask me before anything destructive or irreversible. When done, verify with fleet doctor and fleet drift, then tell me what you changed.\n\n")
	b.WriteString("What the fleet TUI sees now:\n")
	meta := []string{n.OS, n.Role, n.State}
	if n.Address != "" {
		meta = append(meta, n.Address)
	}
	if n.Runtime != "" {
		meta = append(meta, "containers via "+n.Runtime)
	}
	fmt.Fprintf(&b, "- node %s: %s\n", n.Name, strings.Join(meta, ", "))
	if len(problems) == 0 {
		b.WriteString("- no problems detected\n")
	}
	for _, p := range problems {
		b.WriteString("- " + p + "\n")
	}
	if note = strings.TrimSpace(note); note != "" {
		b.WriteString("\nI also want: " + note + "\n")
	}
	return b.String()
}
