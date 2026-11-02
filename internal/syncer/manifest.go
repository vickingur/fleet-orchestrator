package syncer

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/BurntSushi/toml"
)

// Merge strategies for single-file rules.
const (
	MergeReplace = "replace" // fleet owns the whole file
	MergeBlock   = "block"   // fleet owns one marked block inside a text file
	MergeJSON    = "json"    // overlay keys merged into a JSON document
	MergeTOML    = "toml"    // overlay keys merged into a TOML document
)

// FileRule maps one bundle file onto one target.
type FileRule struct {
	Source string `toml:"source"`
	Target string `toml:"target"`
	Merge  string `toml:"merge"`
}

// TreeRule copies every file under a bundle directory into a target directory.
// Files already in the target that fleet never wrote are left alone.
type TreeRule struct {
	Source  string   `toml:"source"`
	Target  string   `toml:"target"`
	Exclude []string `toml:"exclude"`
}

// Ensure is a state that files alone cannot produce, such as an installed
// plugin. Check exits 0 when the state holds; Fix establishes it.
type Ensure struct {
	Name  string `toml:"name"`
	Check string `toml:"check"`
	Fix   string `toml:"fix"`
	// Snapshot lists the files the fix changes outside fleet's own targets,
	// such as a plugin registry. Each is copied into the apply's backup set
	// before the fix runs, so fleet restore can undo what the fix did.
	Snapshot []string `toml:"snapshot"`
}

// Manifest declares what the shared layer contains and where it lands.
type Manifest struct {
	Files   []FileRule `toml:"file"`
	Trees   []TreeRule `toml:"tree"`
	Ensures []Ensure   `toml:"ensure"`
}

// alwaysExcluded never ships, whatever a rule says: VCS metadata, OS litter
// and build caches are machine state, not shared configuration.
var alwaysExcluded = []string{".git", ".DS_Store", "__pycache__", "*.pyc", ".ruff_cache", ".pytest_cache", "node_modules"}

// ParseManifest decodes and validates manifest TOML.
func ParseManifest(data []byte) (*Manifest, error) {
	var m Manifest
	meta, err := toml.Decode(string(data), &m)
	if err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		return nil, fmt.Errorf("parse manifest: unknown key %s", undecoded[0])
	}
	var errs []error
	for i, f := range m.Files {
		switch f.Merge {
		case MergeReplace, MergeBlock, MergeJSON, MergeTOML:
		default:
			errs = append(errs, fmt.Errorf("file[%d]: merge must be replace, block, json or toml", i))
		}
		errs = append(errs, checkPaths(fmt.Sprintf("file[%d]", i), f.Source, f.Target))
	}
	for i, t := range m.Trees {
		errs = append(errs, checkPaths(fmt.Sprintf("tree[%d]", i), t.Source, t.Target))
		for _, pat := range t.Exclude {
			// path.Match only reports a malformed pattern when it is used;
			// unchecked, a typo would silently exclude nothing.
			if _, err := path.Match(pat, ""); err != nil {
				errs = append(errs, fmt.Errorf("tree[%d]: exclude pattern %q: %w", i, pat, err))
			}
		}
	}
	names := map[string]bool{}
	for i, e := range m.Ensures {
		if e.Name == "" || e.Check == "" || e.Fix == "" {
			errs = append(errs, fmt.Errorf("ensure[%d]: name, check and fix are required", i))
		}
		if names[e.Name] {
			errs = append(errs, fmt.Errorf("ensure[%d]: duplicate name %q", i, e.Name))
		}
		names[e.Name] = true
		for _, s := range e.Snapshot {
			errs = append(errs, checkTarget(fmt.Sprintf("ensure[%d]: snapshot", i), s))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return &m, nil
}

func checkPaths(where, source, target string) error {
	var errs []error
	if source == "" || path.IsAbs(source) || path.Clean(source) != source || strings.HasPrefix(source, "..") {
		errs = append(errs, fmt.Errorf("%s: source %q must be a clean relative path inside the bundle", where, source))
	}
	errs = append(errs, checkTarget(where, target))
	return errors.Join(errs...)
}

func checkTarget(where, target string) error {
	if !strings.HasPrefix(target, "~/") || path.Clean(target) != target || strings.Contains(target, "/../") {
		return fmt.Errorf("%s: target %q must be a clean path under ~/", where, target)
	}
	return nil
}

// IsMachineState reports whether a file or directory name is VCS metadata,
// OS litter or a build cache, which never belongs in the shared layer.
func IsMachineState(name string) bool {
	for _, pat := range alwaysExcluded {
		if ok, _ := path.Match(pat, name); ok { // patterns are constants, validated by tests
			return true
		}
	}
	return false
}

func (t TreeRule) excluded(rel string) bool {
	for _, part := range strings.Split(rel, "/") {
		if IsMachineState(part) {
			return true
		}
		for _, pat := range t.Exclude {
			if ok, _ := path.Match(pat, part); ok { // validated in ParseManifest
				return true
			}
		}
	}
	for _, pat := range t.Exclude {
		if ok, _ := path.Match(pat, rel); ok {
			return true
		}
	}
	return false
}

func (t TreeRule) id() string { return "tree:" + t.Source + "->" + t.Target }
func (f FileRule) id() string { return f.Merge + ":" + f.Source + "->" + f.Target }
