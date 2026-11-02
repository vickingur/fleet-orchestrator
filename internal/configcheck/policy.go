package configcheck

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/tailscale/hujson"
)

// policyFile is an optional tailnet access policy kept with the config. Fleet
// never uploads it; the check enforces its safety invariants before the
// owner applies it in the Tailscale admin console or through GitOps.
const policyFile = "policy/tailnet.hujson"

type tailnetPolicy struct {
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
	Tests []policyTest `json:"tests"`
}

type policyTest struct {
	Src  string   `json:"src"`
	Deny []string `json:"deny"`
}

func (c *checker) policy() {
	raw, err := os.ReadFile(filepath.Join(c.dir, policyFile))
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		c.fail("policy", "%v", err)
		return
	}
	std, err := hujson.Standardize(raw)
	if err != nil {
		c.fail("policy", "%s is not valid HuJSON: %v", policyFile, err)
		return
	}
	var p tailnetPolicy
	dec := json.NewDecoder(bytes.NewReader(std))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		c.fail("policy", "%s has an unexpected shape: %v", policyFile, err)
		return
	}
	onlyAgents := func(dst []string) bool {
		return len(dst) > 0 && !slices.ContainsFunc(dst, func(d string) bool { return d != "tag:agent" })
	}
	// Agent containers reach each other, never the owner's machines.
	for i, g := range p.Grants {
		if slices.Contains(g.Src, "tag:agent") && !onlyAgents(g.Dst) {
			c.fail("policy", "grant %d lets tag:agent reach %v", i, g.Dst)
		}
		if slices.Contains(g.Src, "*") {
			c.fail("policy", "grant %d has a wildcard source, which includes agents", i)
		}
	}
	for i, r := range p.SSH {
		if slices.Contains(r.Src, "tag:agent") && !onlyAgents(r.Dst) {
			c.fail("policy", "ssh rule %d lets tag:agent into %v", i, r.Dst)
		}
		if slices.Contains(r.Users, "root") && r.Action != "check" {
			c.fail("policy", "ssh rule %d grants root with action %q, want check", i, r.Action)
		}
	}
	// Tailscale runs the policy's own tests when it is saved; without a denial
	// test for agents, a rule that lets them out would be accepted.
	if !slices.ContainsFunc(p.Tests, func(tc policyTest) bool { return tc.Src == "tag:agent" && len(tc.Deny) > 0 }) {
		c.fail("policy", "no policy test asserts that tag:agent is denied the owner's machines")
	}
	if len(p.TagOwners["tag:agent"]) == 0 {
		c.fail("policy", "tag:agent has no owner, so no auth key could carry it")
	}
}
