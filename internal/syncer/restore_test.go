package syncer

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestListBackupsIsEmptyWithoutRoot(t *testing.T) {
	sets, err := ListBackups(filepath.Join(t.TempDir(), "missing"))
	if err != nil || sets != nil {
		t.Fatalf("sets=%v err=%v", sets, err)
	}
}

func TestRestorePutsFilesBackAndSavesTheCurrentOnes(t *testing.T) {
	root, home := filepath.Join(t.TempDir(), "backup"), filepath.Join(t.TempDir(), "home")
	writeFile(t, filepath.Join(root, "setA/.claude/settings.json"), "old\n")
	writeFile(t, filepath.Join(root, "setA/.codex/config.toml"), "old toml\n")
	// A backup copy carries the original mode; a 0755 script deleted by apply comes back executable.
	writeFile(t, filepath.Join(root, "setA/.local/bin/tool"), "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(root, "setA/.local/bin/tool"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "setB/.claude/settings.json"), "older\n")
	// A staging file from an apply killed mid-write is not a saved target.
	writeFile(t, filepath.Join(root, "setB/.claude/.fleet-tmp-123"), "partial")
	writeFile(t, filepath.Join(home, ".claude/settings.json"), "current\n")
	if err := os.Chmod(filepath.Join(home, ".claude/settings.json"), 0o600); err != nil {
		t.Fatal(err)
	}

	sets, err := ListBackups(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []BackupSet{
		{Name: "setA", Files: []string{"~/.claude/settings.json", "~/.codex/config.toml", "~/.local/bin/tool"}},
		{Name: "setB", Files: []string{"~/.claude/settings.json"}},
	}
	if !reflect.DeepEqual(sets, want) {
		t.Fatalf("sets = %+v", sets)
	}

	saved := filepath.Join(root, "setC")
	restored, err := Restore(root, "setA", home, nil, saved)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored, want[0].Files) {
		t.Fatalf("restored = %v", restored)
	}
	if got := readFile(t, filepath.Join(home, ".claude/settings.json")); got != "old\n" {
		t.Fatalf("settings after restore: %q", got)
	}
	if got := readFile(t, filepath.Join(home, ".codex/config.toml")); got != "old toml\n" {
		t.Fatalf("config created from backup: %q", got)
	}
	if info, _ := os.Stat(filepath.Join(home, ".claude/settings.json")); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode of the existing file was not kept: %v", info.Mode())
	}
	if info, _ := os.Stat(filepath.Join(home, ".local/bin/tool")); info.Mode().Perm() != 0o755 {
		t.Fatalf("mode of the recreated file was not taken from the backup: %v", info.Mode())
	}
	// The replaced content is in the new set; a file that did not exist leaves nothing there.
	if got := readFile(t, filepath.Join(saved, ".claude/settings.json")); got != "current\n" {
		t.Fatalf("saved current content: %q", got)
	}
	if _, err := os.Stat(filepath.Join(saved, ".codex/config.toml")); err == nil {
		t.Fatal("a file that did not exist before was 'saved'")
	}
}

func TestRestoreWritesThroughASymlinkedTarget(t *testing.T) {
	root, home, dotfiles := filepath.Join(t.TempDir(), "backup"), filepath.Join(t.TempDir(), "home"), filepath.Join(t.TempDir(), "dotfiles")
	writeFile(t, filepath.Join(root, "s/.claude/settings.json"), "old\n")
	writeFile(t, filepath.Join(dotfiles, "settings.json"), "current\n")
	link := filepath.Join(home, ".claude/settings.json")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dotfiles, "settings.json"), link); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(root, "s", home, nil, filepath.Join(root, "n")); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink replaced by a regular file: %v", info.Mode())
	}
	if got := readFile(t, filepath.Join(dotfiles, "settings.json")); got != "old\n" {
		t.Fatalf("link target not restored: %q", got)
	}
	if got := readFile(t, filepath.Join(root, "n/.claude/settings.json")); got != "current\n" {
		t.Fatalf("replaced content not saved: %q", got)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/nonexistent/settings.json", link); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(root, "s", home, nil, filepath.Join(root, "n2")); err == nil || !strings.Contains(err.Error(), "cannot be followed") {
		t.Fatalf("dangling link: %v", err)
	}
}

func TestBackupFileKeepsModeUnderARestrictiveUmask(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	home, set := t.TempDir(), filepath.Join(t.TempDir(), "set")
	file := filepath.Join(home, "tool")
	writeFile(t, file, "#!/bin/sh\n")
	if err := os.Chmod(file, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := BackupFile(file, "~/tool", set); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(set, "tool"))
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("backup mode = %v, %v", info.Mode(), err)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(set, ".fleet-tmp-*")); len(leftovers) != 0 {
		t.Fatalf("temp files left behind: %v", leftovers)
	}
}

func TestBackupFileKeepsTheFirstCopyInASet(t *testing.T) {
	home, set := t.TempDir(), filepath.Join(t.TempDir(), "set")
	file := filepath.Join(home, "registry.json")
	writeFile(t, file, "original")
	if saved, err := BackupFile(file, "~/registry.json", set); err != nil || !saved {
		t.Fatalf("first: saved=%v err=%v", saved, err)
	}
	writeFile(t, file, "after first fix")
	if saved, err := BackupFile(file, "~/registry.json", set); err != nil || !saved {
		t.Fatalf("second: saved=%v err=%v", saved, err)
	}
	if got := readFile(t, filepath.Join(set, "registry.json")); got != "original" {
		t.Fatalf("second backup overwrote the first: %q", got)
	}
}

func TestRestoreOnlyNamedKeysAndRejectsUnknownOnes(t *testing.T) {
	root, home := filepath.Join(t.TempDir(), "backup"), filepath.Join(t.TempDir(), "home")
	writeFile(t, filepath.Join(root, "s/.claude/a"), "a")
	writeFile(t, filepath.Join(root, "s/.claude/b"), "b")

	restored, err := Restore(root, "s", home, []string{"~/.claude/b"}, filepath.Join(root, "n"))
	if err != nil || !reflect.DeepEqual(restored, []string{"~/.claude/b"}) {
		t.Fatalf("restored=%v err=%v", restored, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude/a")); err == nil {
		t.Fatal("a file outside the filter was restored")
	}
	if _, err := Restore(root, "s", home, []string{"~/.claude/zzz"}, filepath.Join(root, "n")); err == nil || !strings.Contains(err.Error(), "does not hold") {
		t.Fatalf("unknown key: %v", err)
	}
	for _, bad := range []string{"s/../s", ".", "..", ""} {
		if _, err := Restore(root, bad, home, nil, filepath.Join(root, "n")); err == nil {
			t.Fatalf("set name %q was accepted", bad)
		}
	}
	if _, err := Restore(root, "missing", home, nil, filepath.Join(root, "n")); err == nil {
		t.Fatal("a missing set was accepted")
	}
}
