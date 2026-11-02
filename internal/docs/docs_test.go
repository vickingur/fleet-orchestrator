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
		"for example `b:builder`.\n\n## Transport",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestRenderOmitsContainerAddressingWithoutAContainerHost(t *testing.T) {
	inv, err := inventory.Parse([]byte(`
[[node]]
name = "a"
host = "host-a"
user = "u"
os = "darwin"
role = "controller"
`))
	if err != nil {
		t.Fatal(err)
	}
	out := string(Render(inv))
	if strings.Contains(out, "Agent containers are addressed") {
		t.Error("guide shows container addressing for a fleet with no container host")
	}
	if !strings.Contains(out, "| a | host-a | darwin | controller | - | u |  |\n\n## Transport") {
		t.Error("node table is not followed by the Transport section")
	}
}

// TestCommittedFleetMDIsCurrent fails when the example inventory or the
// template changed without regenerating the example's FLEET.md.
func TestCommittedFleetMDIsCurrent(t *testing.T) {
	src, err := os.ReadFile("../../examples/config/fleet.toml")
	if err != nil {
		t.Fatal(err)
	}
	inv, err := inventory.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := os.ReadFile("../../examples/config/FLEET.md")
	if err != nil {
		t.Fatalf("examples/config/FLEET.md missing; run fleet docs there (%v)", err)
	}
	if string(committed) != string(Render(inv)) {
		t.Fatal("examples/config/FLEET.md is stale; run fleet docs there")
	}
}
