package syncer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// stateVersion 2 added Introduced; version 1 files are upgraded on load.
const stateVersion = 2

// FileState records what fleet last wrote to a target.
type FileState struct {
	Rule string `json:"rule"`
	// Hash is the sha256 of content fleet wrote, for whole-file rules.
	Hash string `json:"hash,omitempty"`
	// Overlay is the overlay source fleet last merged, for json/toml rules, so
	// keys dropped from the overlay can be withdrawn on the next apply.
	Overlay string `json:"overlay,omitempty"`
	// Merge is the rule's strategy, kept so a removed rule can be undone.
	Merge string `json:"merge"`
	// Introduced lists the keys and array items fleet added to a json/toml
	// target; only these are ever withdrawn.
	Introduced []Contribution `json:"introduced,omitempty"`
}

// State is fleet's per-machine record of managed targets, keyed by "~/..." path.
type State struct {
	Version int                  `json:"version"`
	Files   map[string]FileState `json:"files"`
}

// LoadState reads the state file. A missing file is an empty state; a corrupt
// or future-version file is an error, never silently reset. A version 1 file is
// upgraded using the backups under backupRoot.
func LoadState(path, backupRoot string) (State, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return State{Version: stateVersion, Files: map[string]FileState{}}, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("read state: %w", err)
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return State{}, fmt.Errorf("state file %s is corrupt (%w); move it aside to rebuild, every target will then be treated as unmanaged", path, err)
	}
	if st.Files == nil {
		st.Files = map[string]FileState{}
	}
	switch st.Version {
	case stateVersion:
	case 1:
		if err := upgradeV1(&st, backupRoot); err != nil {
			return State{}, err
		}
	default:
		return State{}, fmt.Errorf("state file %s has version %d; this fleet understands %d", path, st.Version, stateVersion)
	}
	return st, nil
}

// SaveState writes the state atomically.
func SaveState(path string, st State) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(path, append(data, '\n'), 0o600)
}

func sha(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// writeAtomic writes via a temp file in the same directory and renames it into
// place, so a crash never leaves a half-written config.
func writeAtomic(path string, data []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".fleet-tmp-*")
	if err != nil {
		return err
	}
	// After a successful rename the temp path no longer exists, so this
	// cleanup only matters on failure and its own error carries no information.
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if err := tmp.Chmod(mode); err != nil {
		return errors.Join(err, tmp.Close())
	}
	// Flush to disk before the rename makes it visible, so a power loss
	// leaves either the old file or the complete new one, never an empty one.
	if err := tmp.Sync(); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}

// resolveTarget follows a symlinked target to the file it points at, so fleet
// updates the real file (often in a dotfiles repo) instead of replacing the link.
func resolveTarget(abs string) (string, error) {
	info, err := os.Lstat(abs)
	if errors.Is(err, fs.ErrNotExist) {
		return abs, nil
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&fs.ModeSymlink == 0 {
		return abs, nil
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("target is a symlink that cannot be followed (%w); fix or remove the link", err)
	}
	return real, nil
}
