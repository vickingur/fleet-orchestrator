package syncer

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// BackupSet is one directory under the backup root: the files fleet saved
// before one apply, or one ensure fix, changed them.
type BackupSet struct {
	Name  string   `json:"name"`
	Files []string `json:"files"` // "~/..." keys, sorted
}

// ListBackups reads the backup root. A missing root is an empty list.
func ListBackups(root string) ([]BackupSet, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var sets []BackupSet
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		files, err := backupFiles(filepath.Join(root, e.Name()))
		if err != nil {
			return nil, err
		}
		sets = append(sets, BackupSet{Name: e.Name(), Files: files})
	}
	sort.Slice(sets, func(i, j int) bool { return sets[i].Name < sets[j].Name })
	return sets, nil
}

func backupFiles(dir string) ([]string, error) {
	var keys []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		// An apply killed mid-write leaves writeAtomic's staging file behind;
		// it is not a saved target.
		if strings.HasPrefix(d.Name(), ".fleet-tmp-") {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		keys = append(keys, "~/"+filepath.ToSlash(rel))
		return nil
	})
	sort.Strings(keys)
	return keys, err
}

// Restore copies the files of one backup set back under home. With only set,
// just those "~/..." keys are restored, and a key the set does not hold is an
// error. Each current file is saved into newBackupDir first, so a restore is
// itself undoable. A symlinked destination is written through, like apply. An
// existing destination keeps its mode; a missing one gets the mode the backup
// copy carries, which is the original file's. Fleet's state is not touched: a
// restored managed file reads as a local edit on the next apply, which is the
// protection wanted here.
func Restore(root, set, home string, only []string, newBackupDir string) ([]string, error) {
	dir := filepath.Join(root, set)
	if set == "" || set == "." || set == ".." || set != filepath.Base(set) {
		return nil, fmt.Errorf("backup set %q must be a plain directory name", set)
	}
	if _, err := os.Stat(dir); err != nil {
		return nil, fmt.Errorf("backup set %s: %w", set, err)
	}
	keys, err := backupFiles(dir)
	if err != nil {
		return nil, err
	}
	if len(only) > 0 {
		have := map[string]bool{}
		for _, k := range keys {
			have[k] = true
		}
		for _, k := range only {
			if !have[k] {
				return nil, fmt.Errorf("backup set %s does not hold %s", set, k)
			}
		}
		keys = only
	}
	var restored []string
	for _, key := range keys {
		rel := filepath.FromSlash(strings.TrimPrefix(key, "~/"))
		src := filepath.Join(dir, rel)
		dst, err := resolveTarget(filepath.Join(home, rel))
		if err != nil {
			return restored, fmt.Errorf("%s: %w", key, err)
		}
		data, err := os.ReadFile(src)
		if err != nil {
			return restored, err
		}
		srcInfo, err := os.Stat(src)
		if err != nil {
			return restored, err
		}
		mode := srcInfo.Mode().Perm()
		if info, err := os.Stat(dst); err == nil {
			mode = info.Mode().Perm()
		}
		if _, err := BackupFile(dst, key, newBackupDir); err != nil {
			return restored, fmt.Errorf("%s: save current content: %w", key, err)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return restored, err
		}
		if err := writeAtomic(dst, data, mode); err != nil {
			return restored, fmt.Errorf("%s: %w", key, err)
		}
		restored = append(restored, key)
	}
	return restored, nil
}
