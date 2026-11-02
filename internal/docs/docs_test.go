package docs

import (
	"os"
	"strings"
	"testing"

	"github.com/vickingur/fleet-orchestrator/internal/inventory"
)

func TestRenderListsEveryNodeAndEscapesCells(t *testing.T) {
	inv, err := inventory.Parse([]byte(`
[[node]]
name = "a"
host = "host-a"
user = "u"
os = "darwin"
role = "controller"
notes = "pipe | inside"

[[node]]
name = "b"
host = "host-b"
user = "v"
os = "linux"
role = "member"
runtime = "podman"
`))
	if err != nil {
		t.Fatal(err)
	}
	out := string(Render(inv))
	for _, want := range []string{
		"| a | host-a | darwin | controller | - | u | pipe \\| inside |",
		"| b | host-b | linux | member | podman | v |  |",
		"fleet exec [--timeout 10m] [--detach] <target> -- <command>",
		"Do not use sudo",
		"Tailscale discovery is optional",
		"this local machine's inventory name",
		"unknown or absent tailnet",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
}

// TestCommittedFleetMDIsCurrent fails when fleet.toml changed without
// regenerating FLEET.md, so the agent contract can never go stale.
func TestCommittedFleetMDIsCurrent(t *testing.T) {
	src, err := os.ReadFile("../../fleet.toml")
	if err != nil {
		t.Fatal(err)
	}
	inv, err := inventory.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := os.ReadFile("../../FLEET.md")
	if err != nil {
		t.Fatalf("FLEET.md missing; run: fleet docs (%v)", err)
	}
	if string(committed) != string(Render(inv)) {
		t.Fatal("FLEET.md is stale; run: fleet docs")
	}
}
