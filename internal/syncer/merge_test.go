package syncer

import (
	"reflect"
	"strings"
	"testing"
)

func m(kv ...any) map[string]any {
	out := map[string]any{}
	for i := 0; i < len(kv); i += 2 {
		out[kv[i].(string)] = kv[i+1]
	}
	return out
}

func l(items ...any) []any { return items }

// merge layers overlay onto base, ignoring what was contributed.
func merge(base, overlay any) any {
	out, err := mergeTracked(base, overlay, nil, func(Contribution) error { return nil })
	if err != nil {
		panic(err)
	}
	return out
}

func TestMergeLayersOverlay(t *testing.T) {
	base := m("theme", "dark", "hooks", m("Stop", l("local")), "perm", m("allow", l("a")), "n", 1.0)
	overlay := m("hooks", m("Stop", l("shared"), "Start", l("s")), "perm", m("allow", l("a", "b")), "n", 2.0, "new", true)
	got := merge(base, overlay)
	want := m("theme", "dark", "hooks", m("Stop", l("local", "shared"), "Start", l("s")),
		"perm", m("allow", l("a", "b")), "n", 2.0, "new", true)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merge =\n%#v\nwant\n%#v", got, want)
	}
	if !reflect.DeepEqual(base["hooks"], m("Stop", l("local"))) {
		t.Fatal("merge mutated base")
	}
}

func TestMergeArrayUnionUsesDeepEquality(t *testing.T) {
	hook := m("type", "command", "command", "bash x.sh")
	base := m("Stop", l(m("hooks", l(hook))))
	got := merge(base, m("Stop", l(m("hooks", l(m("type", "command", "command", "bash x.sh"))))))
	if n := len(got.(map[string]any)["Stop"].([]any)); n != 1 {
		t.Fatalf("identical hook group duplicated: %d entries", n)
	}
}

func TestMergeTypeMismatchOverlayWins(t *testing.T) {
	if got := merge(m("a", "scalar"), m("a", m("b", 1.0))); !reflect.DeepEqual(got, m("a", m("b", 1.0))) {
		t.Fatalf("got %#v", got)
	}
}

func TestUnmergeWithdrawsDroppedKeys(t *testing.T) {
	prev := m("remote", true, "perm", m("allow", l("a", "b")), "nested", m("x", 1.0, "y", 2.0))
	next := m("perm", m("allow", l("a")), "nested", m("x", 1.0))
	base := merge(m("theme", "dark", "perm", m("allow", l("local"))), prev)
	got := merge(unmerge(base, prev, next), next)
	want := m("theme", "dark", "perm", m("allow", l("local", "a")), "nested", m("x", 1.0))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got\n%#v\nwant\n%#v", got, want)
	}
}

func TestUnmergeKeepsLocallyEditedValues(t *testing.T) {
	prev := m("effort", "high", "keep", "x")
	base := m("effort", "max", "keep", "x") // user changed effort after fleet set it
	got := unmerge(base, prev, absent)
	if !reflect.DeepEqual(got, m("effort", "max")) {
		t.Fatalf("got %#v", got)
	}
}

func TestUnmergeLeavesKeysStillInOverlay(t *testing.T) {
	prev := m("a", 1.0)
	if got := unmerge(m("a", 1.0), prev, m("a", 2.0)); !reflect.DeepEqual(got, m("a", 1.0)) {
		t.Fatalf("got %#v", got)
	}
}

func TestDiffPaths(t *testing.T) {
	before := m("a", 1.0, "b", m("c", "x"), "list", l(1.0), "gone", true)
	after := m("a", 2.0, "b", m("c", "x", "d.e", "y"), "list", l(1.0, 2.0, 3.0), "new", false)
	got := strings.Join(diffPaths(before, after), " ")
	want := `+b."d.e" +new -gone ~a ~list[+2]`
	if got != want {
		t.Fatalf("diffPaths = %q, want %q", got, want)
	}
}

func TestNormalizeTOMLArraysOfTables(t *testing.T) {
	got := normalize(map[string]any{"t": []map[string]any{{"a": int64(1)}}})
	if _, ok := got.(map[string]any)["t"].([]any); !ok {
		t.Fatalf("array of tables not normalized: %#v", got)
	}
}

func TestMergeBlock(t *testing.T) {
	block := renderBlock("shared v1")
	cases := map[string]struct{ doc, content, want string }{
		"empty":    {"", "shared v1", block + "\n"},
		"blank":    {"\n\n", "shared v1", block + "\n"},
		"append":   {"# mine\nlocal text\n", "shared v1", "# mine\nlocal text\n\n" + block + "\n"},
		"replace":  {"top\n\n" + block + "\nbottom\n", "shared v2", "top\n\n" + renderBlock("shared v2") + "\nbottom\n"},
		"reworded": {"<!-- fleet:begin old wording -->\nold\n<!-- fleet:end -->\n", "new", renderBlock("new") + "\n"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := mergeBlock(c.doc, c.content)
			if err != nil || got != c.want {
				t.Fatalf("got %q, %v\nwant %q", got, err, c.want)
			}
		})
	}
}

func TestMergeBlockRejectsMalformedMarkers(t *testing.T) {
	for name, doc := range map[string]string{
		"begin only": "<!-- fleet:begin -->\nx\n",
		"end only":   "x\n<!-- fleet:end -->\n",
		"two blocks": renderBlock("a") + "\n" + renderBlock("b"),
	} {
		if _, err := mergeBlock(doc, "c"); err == nil {
			t.Errorf("%s: malformed markers accepted", name)
		}
	}
}

func TestRemoveBlock(t *testing.T) {
	cases := map[string]struct{ doc, want string }{
		"only block":   {renderBlock("x") + "\n", ""},
		"after text":   {"mine\n\n" + renderBlock("x") + "\n", "mine\n"},
		"between text": {"top\n\n" + renderBlock("x") + "\n\nbottom\n", "top\n\nbottom\n"},
		"no block":     {"untouched\n", "untouched\n"},
	}
	for name, c := range cases {
		got, err := removeBlock(c.doc)
		if err != nil || got != c.want {
			t.Errorf("%s: got %q, %v want %q", name, got, err, c.want)
		}
	}
}

func TestMergeBlockRoundTripsWithRemove(t *testing.T) {
	doc := "# local notes\n"
	merged, _ := mergeBlock(doc, "shared")
	back, _ := removeBlock(merged)
	if back != doc {
		t.Fatalf("append then remove = %q, want %q", back, doc)
	}
}
