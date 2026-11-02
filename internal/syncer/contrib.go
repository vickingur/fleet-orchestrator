package syncer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

// Contribution is one thing an overlay added that was not there before: a key
// (Item empty) or an item appended to an array. Only contributions are ever
// withdrawn, so a key the machine already had is never deleted by fleet, even
// when it held the same value as the overlay.
type Contribution struct {
	Path []string        `json:"path"`
	Item json.RawMessage `json:"item,omitempty"`
}

func canon(v any) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode value for tracking: %w", err)
	}
	return b, nil
}

func (c Contribution) key() string { return strings.Join(c.Path, "\x00") + "\x01" + string(c.Item) }

// mergeTracked is merge that also reports what it added.
func mergeTracked(base, overlay any, path []string, add func(Contribution) error) (any, error) {
	bm, baseIsMap := base.(map[string]any)
	om, overlayIsMap := overlay.(map[string]any)
	if baseIsMap && overlayIsMap {
		out := make(map[string]any, len(bm)+len(om))
		for k, v := range bm {
			out[k] = v
		}
		for _, k := range sortedKeys(om) {
			p := append(append([]string(nil), path...), k)
			if bv, ok := out[k]; ok {
				v, err := mergeTracked(bv, om[k], p, add)
				if err != nil {
					return nil, err
				}
				out[k] = v
				continue
			}
			out[k] = om[k]
			if err := add(Contribution{Path: p}); err != nil {
				return nil, err
			}
		}
		return out, nil
	}
	ba, baseIsList := base.([]any)
	oa, overlayIsList := overlay.([]any)
	if baseIsList && overlayIsList {
		out := append([]any(nil), ba...)
		for _, item := range oa {
			if contains(out, item) {
				continue
			}
			out = append(out, item)
			raw, err := canon(item)
			if err != nil {
				return nil, err
			}
			if err := add(Contribution{Path: append([]string(nil), path...), Item: raw}); err != nil {
				return nil, err
			}
		}
		return out, nil
	}
	return overlay, nil
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func lookup(v any, path []string) (any, bool) {
	for _, k := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		if v, ok = m[k]; !ok {
			return nil, false
		}
	}
	return v, true
}

func parentOf(v any, path []string) (map[string]any, bool) {
	p, ok := lookup(v, path[:len(path)-1])
	if !ok {
		return nil, false
	}
	m, ok := p.(map[string]any)
	return m, ok
}

// withdraw removes the contributions the next overlay no longer makes. A key
// is only removed while it still holds the value fleet set; an array item is
// removed wherever it is. Base is modified in place; the contributions that
// remain in force are returned.
func withdraw(base, prev, next any, contribs []Contribution) ([]Contribution, error) {
	var kept []Contribution
	for _, c := range contribs {
		if len(c.Path) == 0 {
			continue
		}
		if len(c.Item) == 0 {
			parent, ok := parentOf(base, c.Path)
			if !ok {
				continue // the user removed it, or its parent, already
			}
			leaf := c.Path[len(c.Path)-1]
			cur, present := parent[leaf]
			if !present {
				continue
			}
			prevVal, _ := lookup(prev, c.Path)
			if nextVal, inNext := lookup(next, c.Path); inNext {
				kept = append(kept, c)
				// The whole subtree is fleet's: drop sub-keys the overlay dropped.
				parent[leaf] = unmerge(cur, prevVal, nextVal)
				continue
			}
			if reflect.DeepEqual(cur, prevVal) {
				delete(parent, leaf)
				continue
			}
			// A block fleet created that the user added to: withdraw fleet's
			// parts and drop the block only if nothing of the user's is left.
			if _, isMap := cur.(map[string]any); isMap {
				reduced := unmerge(cur, prevVal, absent).(map[string]any)
				if len(reduced) == 0 {
					delete(parent, leaf)
				} else {
					parent[leaf] = reduced
				}
			}
			continue
		}
		nextList, _ := lookup(next, c.Path)
		if inList(nextList, c.Item) {
			kept = append(kept, c)
			continue
		}
		parent, ok := parentOf(base, c.Path)
		if !ok {
			continue
		}
		leaf := c.Path[len(c.Path)-1]
		list, ok := parent[leaf].([]any)
		if !ok {
			continue
		}
		out := list[:0:0]
		for _, e := range list {
			raw, err := canon(e)
			if err != nil {
				return nil, err
			}
			if string(raw) != string(c.Item) {
				out = append(out, e)
			}
		}
		parent[leaf] = out
	}
	return kept, nil
}

func inList(v any, item json.RawMessage) bool {
	list, ok := v.([]any)
	if !ok {
		return false
	}
	for _, e := range list {
		if raw, err := canon(e); err == nil && string(raw) == string(item) {
			return true
		}
	}
	return false
}

// structuredNext computes the merged document and the contributions in force.
func structuredNext(base, prev, overlay any, prior []Contribution) (any, []Contribution, error) {
	kept, err := withdraw(base, prev, overlay, prior)
	if err != nil {
		return nil, nil, err
	}
	seen := map[string]bool{}
	for _, c := range kept {
		seen[c.key()] = true
	}
	all := append([]Contribution(nil), kept...)
	next, err := mergeTracked(base, overlay, nil, func(c Contribution) error {
		if !seen[c.key()] {
			seen[c.key()] = true
			all = append(all, c)
		}
		return nil
	})
	return next, all, err
}

// upgradeV1 converts a version 1 state, which recorded only the last overlay,
// by reconstructing what fleet introduced: the overlay merged onto the oldest
// backup of each file (the content before fleet first touched it), or onto
// nothing when fleet created the file and so took no backup.
func upgradeV1(st *State, backupRoot string) error {
	dirs, err := os.ReadDir(backupRoot)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var names []string
	for _, d := range dirs {
		if d.IsDir() {
			names = append(names, d.Name())
		}
	}
	sort.Strings(names)
	for key, fsEntry := range st.Files {
		if fsEntry.Merge != MergeJSON && fsEntry.Merge != MergeTOML {
			continue
		}
		var pre any = map[string]any{}
		for _, n := range names {
			data, err := os.ReadFile(filepath.Join(backupRoot, n, filepath.FromSlash(strings.TrimPrefix(key, "~/"))))
			if err != nil {
				continue
			}
			if pre, err = decode(fsEntry.Merge, data); err != nil {
				return fmt.Errorf("upgrade state for %s: backup %s: %w", key, n, err)
			}
			break
		}
		overlay, err := decode(fsEntry.Merge, []byte(fsEntry.Overlay))
		if err != nil {
			return fmt.Errorf("upgrade state for %s: %w", key, err)
		}
		var contribs []Contribution
		if _, err := mergeTracked(pre, overlay, nil, func(c Contribution) error {
			contribs = append(contribs, c)
			return nil
		}); err != nil {
			return err
		}
		fsEntry.Introduced = contribs
		st.Files[key] = fsEntry
	}
	st.Version = stateVersion
	return nil
}
