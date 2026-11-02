// Package tailnet reads the local Tailscale daemon's view of the network.
package tailnet

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/vickingur/fleet-orchestrator/internal/execx"
)

// Peer is one device as seen by the local tailscaled.
type Peer struct {
	Name     string    // first label of the MagicDNS name, e.g. "worker-example"
	OS       string    // as reported by Tailscale: "macOS", "linux", ...
	IPs      []string  // Tailscale addresses, IPv4 first
	Online   bool      // currently connected to the coordination server
	LastSeen time.Time // zero when online or unknown
	Tags     []string  // ACL tags such as "tag:agent"
	Self     bool      // this machine
}

// Status is the tailnet as seen from this machine.
type Status struct {
	Peers []Peer
}

// Find returns the peer whose MagicDNS name equals host (case-insensitive).
func (s Status) Find(host string) (Peer, bool) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, p := range s.Peers {
		if p.Name == host {
			return p, true
		}
	}
	return Peer{}, false
}

// Self returns this machine's entry.
func (s Status) Self() (Peer, bool) {
	for _, p := range s.Peers {
		if p.Self {
			return p, true
		}
	}
	return Peer{}, false
}

// Tagged returns peers carrying tag, sorted by name.
func (s Status) Tagged(tag string) []Peer {
	var out []Peer
	for _, p := range s.Peers {
		if slices.Contains(p.Tags, tag) {
			out = append(out, p)
		}
	}
	slices.SortFunc(out, func(a, b Peer) int { return strings.Compare(a.Name, b.Name) })
	return out
}

type rawPeer struct {
	DNSName      string    `json:"DNSName"`
	OS           string    `json:"OS"`
	TailscaleIPs []string  `json:"TailscaleIPs"`
	Online       bool      `json:"Online"`
	LastSeen     time.Time `json:"LastSeen"`
	Tags         []string  `json:"Tags"`
}

type rawStatus struct {
	BackendState string              `json:"BackendState"`
	Self         *rawPeer            `json:"Self"`
	Peer         map[string]*rawPeer `json:"Peer"`
}

// Parse decodes `tailscale status --json` output.
func Parse(data []byte) (Status, error) {
	var raw rawStatus
	if err := json.Unmarshal(data, &raw); err != nil {
		return Status{}, fmt.Errorf("parse tailscale status: %w", err)
	}
	if raw.BackendState != "Running" {
		return Status{}, fmt.Errorf("tailscale is %q, not Running; connect with: tailscale up", raw.BackendState)
	}
	if raw.Self == nil {
		return Status{}, fmt.Errorf("tailscale status has no Self entry")
	}
	st := Status{Peers: []Peer{convert(raw.Self, true)}}
	for _, p := range raw.Peer {
		st.Peers = append(st.Peers, convert(p, false))
	}
	slices.SortFunc(st.Peers, func(a, b Peer) int { return strings.Compare(a.Name, b.Name) })
	return st, nil
}

func convert(p *rawPeer, self bool) Peer {
	name, _, _ := strings.Cut(strings.ToLower(p.DNSName), ".")
	out := Peer{Name: name, OS: p.OS, IPs: p.TailscaleIPs, Online: p.Online || self, Tags: p.Tags, Self: self}
	if !p.LastSeen.IsZero() && p.LastSeen.Year() > 1 {
		out.LastSeen = p.LastSeen
	}
	return out
}

// Reader fetches status through the tailscale CLI.
type Reader struct {
	Runner execx.Runner
	// Socket overrides the tailscaled socket (agent containers run their own).
	Socket string
}

// Status queries the local daemon.
func (r Reader) Status(ctx context.Context) (Status, error) {
	args := []string{"status", "--json"}
	if r.Socket != "" {
		args = append([]string{"--socket", r.Socket}, args...)
	}
	out, err := execx.Output(ctx, r.Runner, execx.Cmd{Name: "tailscale", Args: args, Timeout: 10 * time.Second})
	if err != nil {
		return Status{}, fmt.Errorf("tailscale status: %w", err)
	}
	return Parse(out)
}
