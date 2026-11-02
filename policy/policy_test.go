// Package policy holds the tailnet access policy; this test enforces its
// safety invariants locally, before Tailscale ever sees the file.
package policy

import (
	"bytes"
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/tailscale/hujson"
)

type policy struct {
	TagOwners map[string][]string `json:"tagOwners"`
	Grants    []struct {
		Src []string `json:"src"`
		Dst []string `json:"dst"`
		IP  []string `json:"ip"`
	} `json:"grants"`
	SSH []struct {
		Action      string   `json:"action"`
		Src         []string `json:"src"`
		Dst         []string `json:"dst"`
		Users       []string `json:"users"`
		CheckPeriod string   `json:"checkPeriod"`
	} `json:"ssh"`
	Tests []struct {
		Src  string   `json:"src"`
		Deny []string `json:"deny"`
	} `json:"tests"`
}

func load(t *testing.T) policy {
	t.Helper()
	raw, err := os.ReadFile("tailnet.hujson")
	if err != nil {
		t.Fatal(err)
	}
	std, err := hujson.Standardize(raw)
	if err != nil {
		t.Fatalf("not valid HuJSON: %v", err)
	}
	var p policy
	dec := json.NewDecoder(bytes.NewReader(std))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		t.Fatalf("unexpected policy shape: %v", err)
	}
	return p
}

func onlyAgents(dst []string) bool {
	for _, d := range dst {
		if d != "tag:agent" {
			return false
		}
	}
	return len(dst) > 0
}

func TestAgentsCannotReachOwnerMachines(t *testing.T) {
	p := load(t)
	for i, g := range p.Grants {
		if slices.Contains(g.Src, "tag:agent") && !onlyAgents(g.Dst) {
			t.Errorf("grant %d lets tag:agent reach %v", i, g.Dst)
		}
		if slices.Contains(g.Src, "*") {
			t.Errorf("grant %d has a wildcard source, which includes agents", i)
		}
	}
	for i, r := range p.SSH {
		if slices.Contains(r.Src, "tag:agent") && !onlyAgents(r.Dst) {
			t.Errorf("ssh rule %d lets tag:agent into %v", i, r.Dst)
		}
	}
}

func TestRootNeedsCheckMode(t *testing.T) {
	for i, r := range load(t).SSH {
		if slices.Contains(r.Users, "root") && r.Action != "check" {
			t.Errorf("ssh rule %d grants root with action %q, want check", i, r.Action)
		}
	}
}

func TestPolicyTestsCoverEveryHostAddress(t *testing.T) {
	p := load(t)
	var denied []string
	for _, tc := range p.Tests {
		if tc.Src == "tag:agent" {
			denied = append(denied, tc.Deny...)
		}
	}
	for _, want := range []string{"192.0.2.13:22", "192.0.2.14:22"} {
		if !slices.Contains(denied, want) {
			t.Errorf("policy tests do not assert tag:agent is denied %s", want)
		}
	}
	if owners := p.TagOwners["tag:agent"]; len(owners) == 0 {
		t.Error("tag:agent has no owner, so no auth key could carry it")
	}
}
