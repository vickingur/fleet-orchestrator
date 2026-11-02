// Package syncer applies the shared layer (instructions, settings overlays,
// skills) onto a machine's home directory. Whole files fleet owns are guarded
// by content hashes so local edits surface as conflicts instead of being
// overwritten; structured configs get key-level merges that keep local keys.
package syncer

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Action is what applying a change will do.
type Action string

const (
	Unchanged Action = "ok"
	Create    Action = "create"
	Update    Action = "update"
	Merge     Action = "merge"
	Delete    Action = "delete"
	Conflict  Action = "conflict"
	Release   Action = "release"
)

// Change is one planned operation on one target.
type Change struct {
	Target string `json:"target"`
	Action Action `json:"action"`
	Detail string `json:"detail,omitempty"`
	Rule   string `json:"rule"`

	abs   string
	data  []byte
	mode  fs.FileMode
	entry *FileState // state after this change applies; nil removes the entry
}

// Plan is the full set of changes for one machine plus the state to save.
type Plan struct {
	Changes []Change `json:"changes"`
	prior   State
}

// Counts tallies changes by action.
func (p Plan) Counts() map[Action]int {
	out := map[Action]int{}
	for _, c := range p.Changes {
		out[c.Action]++
	}
	return out
}

// Pending reports whether applying would change anything or hit a conflict.
func (p Plan) Pending() bool {
	for _, c := range p.Changes {
		if c.Action != Unchanged {
			return true
		}
	}
	return false
}

// Options locate the bundle and the home directory being managed.
type Options struct {
	Bundle string
	Home   string
	// Force overwrites whole files that were edited locally (after backup).
	Force bool
}

type desired struct {
	key   string
	abs   string
	rule  string
	merge string
	data  []byte
	mode  fs.FileMode
}

func (o Options) abs(target string) string {
	return filepath.Join(o.Home, filepath.FromSlash(strings.TrimPrefix(target, "~/")))
}

// Build computes the plan without touching the filesystem.
func Build(m *Manifest, st State, opt Options) (Plan, error) {
	want, err := collect(m, opt)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{prior: st}
	for _, d := range want {
		var c Change
		switch d.merge {
		case MergeReplace:
			c, err = planReplace(d, st, opt.Force)
		case MergeBlock:
			c, err = planBlock(d)
		case MergeJSON, MergeTOML:
			c, err = planStructured(d, st)
		}
		if err != nil {
			return Plan{}, fmt.Errorf("%s: %w", d.key, err)
		}
		plan.Changes = append(plan.Changes, c)
	}
	orphans, err := planOrphans(want, st, opt)
	if err != nil {
		return Plan{}, err
	}
	plan.Changes = append(plan.Changes, orphans...)
	sort.SliceStable(plan.Changes, func(i, j int) bool { return plan.Changes[i].Target < plan.Changes[j].Target })
	return plan, nil
}

func collect(m *Manifest, opt Options) (map[string]desired, error) {
	want := map[string]desired{}
	add := func(d desired) error {
		if prev, dup := want[d.key]; dup {
			return fmt.Errorf("target %s is claimed by both %s and %s", d.key, prev.rule, d.rule)
		}
		want[d.key] = d
		return nil
	}
	for _, f := range m.Files {
		src := filepath.Join(opt.Bundle, filepath.FromSlash(f.Source))
		info, err := os.Stat(src)
		if err != nil {
			return nil, fmt.Errorf("rule %s: %w", f.id(), err)
		}
		data, err := os.ReadFile(src)
		if err != nil {
			return nil, err
		}
		abs, err := resolveTarget(opt.abs(f.Target))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.Target, err)
		}
		if err := add(desired{key: f.Target, abs: abs, rule: f.id(), merge: f.Merge, data: data, mode: fileMode(info)}); err != nil {
			return nil, err
		}
	}
	for _, t := range m.Trees {
		root := filepath.Join(opt.Bundle, filepath.FromSlash(t.Source))
		err := filepath.WalkDir(root, func(p string, de fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			if rel != "." && t.excluded(rel) {
				if de.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if de.IsDir() {
				return nil
			}
			if !de.Type().IsRegular() {
				return fmt.Errorf("%s: only regular files can be synced", p)
			}
			info, err := de.Info()
			if err != nil {
				return err
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			key := t.Target + "/" + rel
			abs, err := resolveTarget(opt.abs(key))
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			return add(desired{key: key, abs: abs, rule: t.id(), merge: MergeReplace, data: data, mode: fileMode(info)})
		})
		if err != nil {
			return nil, fmt.Errorf("rule %s: %w", t.id(), err)
		}
	}
	return want, nil
}

func fileMode(info fs.FileInfo) fs.FileMode {
	if info.Mode()&0o111 != 0 {
		return 0o755
	}
	return 0o644
}

// readTarget reads a target. abs must already be resolved with resolveTarget.
func readTarget(abs string) (data []byte, mode fs.FileMode, exists bool, err error) {
	info, err := os.Stat(abs)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, 0, false, fmt.Errorf("target is not a regular file")
	}
	data, err = os.ReadFile(abs)
	return data, info.Mode().Perm(), true, err
}

func planReplace(d desired, st State, force bool) (Change, error) {
	c := Change{Target: d.key, Rule: d.rule, abs: d.abs, data: d.data, mode: d.mode}
	entry := &FileState{Rule: d.rule, Merge: MergeReplace, Hash: sha(d.data)}
	cur, mode, exists, err := readTarget(d.abs)
	if err != nil {
		return c, err
	}
	prev, tracked := st.Files[d.key]
	switch {
	case !exists:
		c.Action, c.entry = Create, entry
	case bytes.Equal(cur, d.data) && mode == d.mode:
		c.Action, c.entry = Unchanged, entry
	case bytes.Equal(cur, d.data):
		c.Action, c.entry, c.Detail = Update, entry, fmt.Sprintf("mode %o -> %o", mode, d.mode)
	case tracked && prev.Hash == sha(cur):
		c.Action, c.entry = Update, entry
	case force:
		c.Action, c.entry, c.Detail = Update, entry, "forced over local content (backed up)"
	default:
		c.Action = Conflict
		if tracked {
			c.Detail = "edited locally since fleet last wrote it"
			c.entry = &prev
		} else {
			c.Detail = "exists with different content that fleet did not write"
		}
	}
	return c, nil
}

func planBlock(d desired) (Change, error) {
	c := Change{Target: d.key, Rule: d.rule, abs: d.abs, entry: &FileState{Rule: d.rule, Merge: MergeBlock}}
	cur, mode, exists, err := readTarget(d.abs)
	if err != nil {
		return c, err
	}
	next, err := mergeBlock(string(cur), string(d.data))
	if err != nil {
		return c, err
	}
	c.data, c.mode = []byte(next), 0o644
	if exists {
		c.mode = mode
	}
	switch {
	case !exists:
		c.Action = Create
	case next == string(cur):
		c.Action = Unchanged
	case strings.Contains(string(cur), blockBeginPrefix):
		c.Action, c.Detail = Merge, "managed block updated"
	case strings.TrimSpace(string(cur)) == "":
		c.Action, c.Detail = Merge, "managed block written into empty file"
	default:
		c.Action, c.Detail = Merge, "managed block appended; existing text kept above it"
	}
	return c, nil
}

func decode(format string, data []byte) (any, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]any{}, nil
	}
	var v map[string]any
	var err error
	if format == MergeJSON {
		err = json.Unmarshal(data, &v)
	} else {
		_, err = toml.Decode(string(data), &v)
	}
	if err != nil {
		return nil, fmt.Errorf("not valid %s: %w", strings.ToUpper(format), err)
	}
	return normalize(v), nil
}

func encode(format string, v any) ([]byte, error) {
	if format == MergeJSON {
		data, err := json.MarshalIndent(v, "", "  ")
		return append(data, '\n'), err
	}
	var buf bytes.Buffer
	err := toml.NewEncoder(&buf).Encode(v)
	return buf.Bytes(), err
}

func planStructured(d desired, st State) (Change, error) {
	c := Change{Target: d.key, Rule: d.rule, abs: d.abs}
	overlay, err := decode(d.merge, d.data)
	if err != nil {
		return c, fmt.Errorf("overlay %w", err)
	}
	cur, mode, exists, err := readTarget(d.abs)
	if err != nil {
		return c, err
	}
	// Decode twice: base is reshaped in place, before stays pristine for the diff.
	before, err := decode(d.merge, cur)
	if err != nil {
		return c, fmt.Errorf("target %w", err)
	}
	base, _ := decode(d.merge, cur)
	prior := st.Files[d.key]
	var prev any = map[string]any{}
	if prior.Overlay != "" {
		if prev, err = decode(d.merge, []byte(prior.Overlay)); err != nil {
			return c, fmt.Errorf("recorded overlay %w", err)
		}
	}
	next, contribs, err := structuredNext(base, prev, overlay, prior.Introduced)
	if err != nil {
		return c, err
	}
	c.entry = &FileState{Rule: d.rule, Merge: d.merge, Overlay: string(d.data), Introduced: contribs}
	c.mode = 0o644
	if exists {
		c.mode = mode
	}
	if c.data, err = encode(d.merge, next); err != nil {
		return c, err
	}
	switch {
	case !exists:
		c.Action = Create
	case reflect.DeepEqual(before, next):
		c.Action = Unchanged
	default:
		c.Action, c.Detail = Merge, strings.Join(diffPaths(before, next), " ")
	}
	return c, nil
}

// planOrphans handles targets fleet managed before but the manifest no longer
// produces: owned files are deleted if untouched, merged keys are withdrawn.
func planOrphans(want map[string]desired, st State, opt Options) ([]Change, error) {
	keys := make([]string, 0, len(st.Files))
	for k := range st.Files {
		if _, still := want[k]; !still {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var out []Change
	for _, k := range keys {
		prev := st.Files[k]
		abs, err := resolveTarget(opt.abs(k))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		cur, mode, exists, err := readTarget(abs)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		c := Change{Target: k, Rule: prev.Rule, abs: abs, mode: mode}
		switch {
		case !exists:
			c.Action, c.Detail = Release, "already gone"
		case prev.Merge == MergeReplace && prev.Hash == sha(cur):
			c.Action, c.Detail = Delete, "no longer in the shared layer"
		case prev.Merge == MergeReplace:
			c.Action, c.Detail = Release, "no longer in the shared layer; kept because it was edited locally"
		case prev.Merge == MergeBlock:
			next, err := removeBlock(string(cur))
			if err != nil {
				return nil, fmt.Errorf("%s: %w", k, err)
			}
			c.Action, c.Detail, c.data = Merge, "managed block removed", []byte(next)
		default:
			before, err := decode(prev.Merge, cur)
			if err != nil {
				return nil, fmt.Errorf("%s: target %w", k, err)
			}
			base, _ := decode(prev.Merge, cur)
			prevOverlay, err := decode(prev.Merge, []byte(prev.Overlay))
			if err != nil {
				return nil, fmt.Errorf("%s: recorded overlay %w", k, err)
			}
			if _, err := withdraw(base, prevOverlay, map[string]any{}, prev.Introduced); err != nil {
				return nil, fmt.Errorf("%s: %w", k, err)
			}
			if c.data, err = encode(prev.Merge, base); err != nil {
				return nil, err
			}
			c.Action, c.Detail = Merge, "withdraw shared keys: "+strings.Join(diffPaths(before, base), " ")
			if reflect.DeepEqual(before, base) {
				c.Action, c.Detail = Release, "no shared keys left to withdraw"
			}
		}
		out = append(out, c)
	}
	return out, nil
}
