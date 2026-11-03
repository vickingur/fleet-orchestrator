// Package inventory loads and validates fleet.toml, the single source of truth
// for which machines exist and how to reach them.
package inventory

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// OS is a node's operating system family.
type OS string

const (
	Darwin OS = "darwin"
	Linux  OS = "linux"
)

// Role marks the single node that holds the source checkout and pushes to others.
type Role string

const (
	Controller Role = "controller"
	Member     Role = "member"
)

// Node is one machine in the fleet.
type Node struct {
	Name     string `toml:"name"`
	Host     string `toml:"host"`
	User     string `toml:"user"`
	Port     int    `toml:"port"`
	OS       OS     `toml:"os"`
	Role     Role   `toml:"role"`
	Runtime  string `toml:"runtime"`
	Identity string `toml:"identity"`
	Notes    string `toml:"notes"`
	// Reaches lists the nodes fleet on this node may connect to. Absent, it
	// reaches every node; an empty list reaches none.
	Reaches *[]string `toml:"reaches"`
}

// CanReach reports whether fleet running on n may connect to the named node.
// A node always reaches itself: its commands run locally.
func (n Node) CanReach(name string) bool {
	return n.Reaches == nil || name == n.Name || slices.Contains(*n.Reaches, name)
}

// SSHPort returns the configured port, or the SSH protocol port 22 when unset.
func (n Node) SSHPort() int {
	if n.Port == 0 {
		return 22
	}
	return n.Port
}

// Tool is an agent tool every node is expected to have.
type Tool struct {
	Name    string `toml:"name"`
	Version string `toml:"version"`
}

// Inventory is the parsed fleet.toml.
type Inventory struct {
	Nodes []Node `toml:"node"`
	Tools []Tool `toml:"tool"`
}

var (
	namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)
	// hostPattern admits DNS names and IPv4 addresses; a leading "-" could
	// otherwise be read by ssh as an option.
	hostPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,252}$`)
	userPattern = regexp.MustCompile(`^[a-z_][a-z0-9_.-]{0,31}$`)
)

// Parse decodes and validates inventory TOML. Unknown keys are an error so a
// typo can never silently drop a setting.
func Parse(data []byte) (*Inventory, error) {
	var inv Inventory
	meta, err := toml.Decode(string(data), &inv)
	if err != nil {
		return nil, fmt.Errorf("parse inventory: %w", err)
	}
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, k := range undecoded {
			keys[i] = k.String()
		}
		return nil, fmt.Errorf("parse inventory: unknown keys: %s", strings.Join(keys, ", "))
	}
	if err := inv.validate(); err != nil {
		return nil, err
	}
	return &inv, nil
}

func (inv *Inventory) validate() error {
	var errs []error
	seen := map[string]bool{}
	controllers := 0
	for i, n := range inv.Nodes {
		where := fmt.Sprintf("node[%d] %q", i, n.Name)
		if !namePattern.MatchString(n.Name) {
			errs = append(errs, fmt.Errorf("%s: name must match %s", where, namePattern))
		}
		if seen[n.Name] {
			errs = append(errs, fmt.Errorf("%s: duplicate name", where))
		}
		seen[n.Name] = true
		if n.Host == "" {
			errs = append(errs, fmt.Errorf("%s: host is required", where))
		} else if !hostPattern.MatchString(n.Host) {
			errs = append(errs, fmt.Errorf("%s: host %q must be a DNS name or IPv4 address", where, n.Host))
		}
		if n.User == "" {
			errs = append(errs, fmt.Errorf("%s: user is required", where))
		} else if !userPattern.MatchString(n.User) {
			errs = append(errs, fmt.Errorf("%s: user %q must match %s", where, n.User, userPattern))
		}
		if n.OS != Darwin && n.OS != Linux {
			errs = append(errs, fmt.Errorf("%s: os must be %q or %q", where, Darwin, Linux))
		}
		switch n.Role {
		case Controller:
			controllers++
		case Member:
		default:
			errs = append(errs, fmt.Errorf("%s: role must be %q or %q", where, Controller, Member))
		}
		if n.Runtime != "" && n.Runtime != "podman" {
			errs = append(errs, fmt.Errorf("%s: runtime must be empty or \"podman\"", where))
		}
		if n.Port < 0 || n.Port > 65535 {
			errs = append(errs, fmt.Errorf("%s: port out of range", where))
		}
	}
	if len(inv.Nodes) == 0 {
		errs = append(errs, errors.New("inventory has no nodes"))
	}
	if controllers != 1 {
		errs = append(errs, fmt.Errorf("inventory needs exactly one controller node, found %d", controllers))
	}
	for i, n := range inv.Nodes {
		if n.Reaches == nil {
			continue
		}
		where := fmt.Sprintf("node[%d] %q: reaches", i, n.Name)
		listed := map[string]bool{}
		for _, r := range *n.Reaches {
			switch {
			case !seen[r]:
				errs = append(errs, fmt.Errorf("%s unknown node %q", where, r))
			case r == n.Name:
				errs = append(errs, fmt.Errorf("%s lists the node itself, which it always reaches", where))
			case listed[r]:
				errs = append(errs, fmt.Errorf("%s lists %q twice", where, r))
			}
			listed[r] = true
		}
		if n.Role != Controller {
			continue
		}
		// push, drift and doctor run on the controller against every node.
		for _, m := range inv.Nodes {
			if m.Name != n.Name && !listed[m.Name] {
				errs = append(errs, fmt.Errorf("%s must include %q: the controller pushes to every node", where, m.Name))
			}
		}
	}
	for i, t := range inv.Tools {
		if t.Name == "" || t.Version == "" {
			errs = append(errs, fmt.Errorf("tool[%d]: name and version command are required", i))
		} else if !namePattern.MatchString(t.Name) {
			errs = append(errs, fmt.Errorf("tool[%d]: name %q must match %s", i, t.Name, namePattern))
		}
	}
	return errors.Join(errs...)
}

// Node returns the node with the given name.
func (inv *Inventory) Node(name string) (Node, error) {
	for _, n := range inv.Nodes {
		if n.Name == name {
			return n, nil
		}
	}
	return Node{}, fmt.Errorf("unknown node %q (known: %s)", name, strings.Join(inv.Names(), ", "))
}

// Names returns all node names in inventory order.
func (inv *Inventory) Names() []string {
	names := make([]string, len(inv.Nodes))
	for i, n := range inv.Nodes {
		names[i] = n.Name
	}
	return names
}

// Target addresses a node, or an agent container on a node ("node:agent").
type Target struct {
	Node  Node
	Agent string
}

// String renders the target in the same syntax Resolve accepts.
func (t Target) String() string {
	if t.Agent == "" {
		return t.Node.Name
	}
	return t.Node.Name + ":" + t.Agent
}

// Resolve turns a target expression into targets. "all" means every node;
// "node:agent" addresses an agent container running on that node; several
// targets can be joined with commas ("laptop,worker").
func (inv *Inventory) Resolve(expr string) ([]Target, error) {
	if strings.Contains(expr, ",") {
		var out []Target
		seen := map[string]bool{}
		for _, part := range strings.Split(expr, ",") {
			if part == "" || part == "all" {
				return nil, fmt.Errorf("invalid target list %q", expr)
			}
			ts, err := inv.Resolve(part)
			if err != nil {
				return nil, err
			}
			if !seen[ts[0].String()] {
				seen[ts[0].String()] = true
				out = append(out, ts[0])
			}
		}
		return out, nil
	}
	if expr == "all" {
		out := make([]Target, len(inv.Nodes))
		for i, n := range inv.Nodes {
			out[i] = Target{Node: n}
		}
		return out, nil
	}
	nodeName, agent, hasAgent := strings.Cut(expr, ":")
	n, err := inv.Node(nodeName)
	if err != nil {
		return nil, err
	}
	if hasAgent {
		if !namePattern.MatchString(agent) {
			return nil, fmt.Errorf("agent name %q must match %s", agent, namePattern)
		}
		if n.Runtime == "" {
			return nil, fmt.Errorf("node %q has no container runtime, so it cannot host agent %q", n.Name, agent)
		}
	}
	return []Target{{Node: n, Agent: agent}}, nil
}

// ValidAgentName reports whether name is usable as an agent container name.
func ValidAgentName(name string) bool { return namePattern.MatchString(name) }

// Controller returns the controller node.
func (inv *Inventory) Controller() Node {
	for _, n := range inv.Nodes {
		if n.Role == Controller {
			return n
		}
	}
	panic("validated inventory has no controller")
}

// SortedNodes returns nodes ordered by name, for stable output.
func (inv *Inventory) SortedNodes() []Node {
	out := append([]Node(nil), inv.Nodes...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
