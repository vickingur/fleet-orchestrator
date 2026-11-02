package syncer

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// Structured values are the generic shapes produced by JSON and TOML decoders:
// map[string]any, []any and scalars. normalize converts decoder-specific
// variants (TOML arrays of tables) into those shapes so merge logic sees one model.
func normalize(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = normalize(e)
		}
		return out
	case []map[string]any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = normalize(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = normalize(e)
		}
		return out
	default:
		return v
	}
}

func contains(list []any, item any) bool {
	for _, e := range list {
		if reflect.DeepEqual(e, item) {
			return true
		}
	}
	return false
}

// absent marks a key that the next overlay no longer contains.
type absentT struct{}

var absent any = absentT{}

// unmerge withdraws what prev contributed to base and next no longer
// contributes. A value is only removed while it still equals what fleet set, so
// a local edit to a formerly managed key survives.
func unmerge(base, prev, next any) any {
	bm, baseIsMap := base.(map[string]any)
	pm, prevIsMap := prev.(map[string]any)
	if baseIsMap && prevIsMap {
		nm, _ := next.(map[string]any)
		out := make(map[string]any, len(bm))
		for k, v := range bm {
			out[k] = v
		}
		for k, pv := range pm {
			bv, present := out[k]
			if !present {
				continue
			}
			nv, inNext := nm[k]
			if !inNext {
				nv = absent
			}
			if nv == absent && reflect.DeepEqual(bv, pv) {
				delete(out, k)
				continue
			}
			out[k] = unmerge(bv, pv, nv)
		}
		return out
	}
	ba, baseIsList := base.([]any)
	pa, prevIsList := prev.([]any)
	if baseIsList && prevIsList {
		na, _ := next.([]any)
		out := make([]any, 0, len(ba))
		for _, item := range ba {
			if contains(pa, item) && !contains(na, item) {
				continue
			}
			out = append(out, item)
		}
		return out
	}
	return base
}

// diffPaths lists dotted key paths that differ between before and after,
// prefixed with + (added), - (removed) or ~ (changed). Output is sorted.
func diffPaths(before, after any) []string {
	var out []string
	var walk func(prefix string, b, a any)
	walk = func(prefix string, b, a any) {
		bm, bIsMap := b.(map[string]any)
		am, aIsMap := a.(map[string]any)
		if bIsMap && aIsMap {
			for k, av := range am {
				p := joinPath(prefix, k)
				if bv, ok := bm[k]; ok {
					walk(p, bv, av)
				} else {
					out = append(out, "+"+p)
				}
			}
			for k := range bm {
				if _, ok := am[k]; !ok {
					out = append(out, "-"+joinPath(prefix, k))
				}
			}
			return
		}
		if !reflect.DeepEqual(b, a) {
			ba, bIsList := b.([]any)
			aa, aIsList := a.([]any)
			if bIsList && aIsList {
				out = append(out, fmt.Sprintf("~%s[%+d]", prefix, len(aa)-len(ba)))
				return
			}
			out = append(out, "~"+prefix)
		}
	}
	walk("", before, after)
	sort.Strings(out)
	return out
}

func joinPath(prefix, key string) string {
	if strings.ContainsAny(key, ". ") {
		key = fmt.Sprintf("%q", key)
	}
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

// Managed-block markers. Matching uses the stable prefixes so the human-facing
// wording can change without orphaning existing blocks.
const (
	blockBeginPrefix = "<!-- fleet:begin"
	blockBegin       = "<!-- fleet:begin · managed by fleet; edit it in the fleet repo, not here -->"
	blockEnd         = "<!-- fleet:end -->"
)

// findBlock returns the byte span of the managed block including its markers.
func findBlock(doc string) (start, end int, found bool, err error) {
	start = strings.Index(doc, blockBeginPrefix)
	if start < 0 {
		if strings.Contains(doc, blockEnd) {
			return 0, 0, false, fmt.Errorf("managed block end marker without begin marker")
		}
		return 0, 0, false, nil
	}
	rel := strings.Index(doc[start:], blockEnd)
	if rel < 0 {
		return 0, 0, false, fmt.Errorf("managed block begin marker without end marker")
	}
	end = start + rel + len(blockEnd)
	if strings.Contains(doc[end:], blockBeginPrefix) {
		return 0, 0, false, fmt.Errorf("more than one managed block")
	}
	return start, end, true, nil
}

func renderBlock(content string) string {
	return blockBegin + "\n" + strings.TrimRight(content, "\n") + "\n" + blockEnd
}

// mergeBlock writes content into doc's managed block, appending a block after
// any existing unmanaged text when none exists. Text outside the block is kept.
func mergeBlock(doc, content string) (string, error) {
	start, end, found, err := findBlock(doc)
	if err != nil {
		return "", err
	}
	block := renderBlock(content)
	if found {
		return doc[:start] + block + doc[end:], nil
	}
	if strings.TrimSpace(doc) == "" {
		return block + "\n", nil
	}
	return strings.TrimRight(doc, "\n") + "\n\n" + block + "\n", nil
}

// removeBlock deletes the managed block and the blank line fleet put before it.
func removeBlock(doc string) (string, error) {
	start, end, found, err := findBlock(doc)
	if err != nil || !found {
		return doc, err
	}
	before := strings.TrimRight(doc[:start], "\n")
	after := strings.TrimLeft(doc[end:], "\n")
	switch {
	case before == "":
		return after, nil
	case after == "":
		return before + "\n", nil
	default:
		return before + "\n\n" + after, nil
	}
}
