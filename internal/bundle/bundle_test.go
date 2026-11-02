package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vickingur/fleet-orchestrator/internal/execx"
)

func TestParsePlatform(t *testing.T) {
	cases := map[string]string{"Linux x86_64": "linux/amd64", "Darwin arm64": "darwin/arm64", "Linux aarch64": "linux/arm64", "Darwin x86_64\n": "darwin/amd64"}
	for in, want := range cases {
		p, err := ParsePlatform(in)
		if err != nil || p.String() != want {
			t.Errorf("%q -> %v, %v; want %s", in, p, err, want)
		}
	}
	for _, bad := range []string{"", "FreeBSD amd64", "Linux riscv64", "Linux"} {
		if _, err := ParsePlatform(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, _ := os.Getwd()
	root, err := FindSource(wd)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestFindSource(t *testing.T) {
	root := repoRoot(t)
	if _, err := os.Stat(filepath.Join(root, "fleet.toml")); err != nil {
		t.Fatalf("found %s without fleet.toml", root)
	}
	if _, err := FindSource(t.TempDir()); err == nil || !strings.Contains(err.Error(), "FLEET_SRC") {
		t.Fatalf("err = %v", err)
	}
}

func TestVersionTracksContent(t *testing.T) {
	src := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(src, filepath.Dir(rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(src, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"fleet.toml", "go.mod", "go.sum", "home/a.md", "container/x", "cmd/fleet/main.go", "internal/p/p.go"} {
		write(f, f)
	}
	v1, err := Version(src)
	if err != nil || len(v1) != 12 {
		t.Fatalf("v1 = %q, %v", v1, err)
	}
	if v, _ := Version(src); v != v1 {
		t.Fatal("version not deterministic")
	}
	write("home/a.md", "changed")
	if v, _ := Version(src); v == v1 {
		t.Fatal("content change did not change version")
	}
	write("internal/p/p_test.go", "test v1")
	v3, _ := Version(src)
	write("internal/p/p_test.go", "test v2")
	if v, _ := Version(src); v != v3 {
		t.Fatal("a test-only change altered the shipped version")
	}
	v2, _ := Version(src)
	write("home/.DS_Store", "junk")
	write("home/skill/__pycache__/m.cpython-313.pyc", "bytecode")
	write("internal/p/node_modules/x/index.js", "dep")
	if v, _ := Version(src); v != v2 {
		t.Fatal("caches or OS litter changed the shipped version")
	}
}

func TestBuildAndPackRealSource(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles fleet")
	}
	root := repoRoot(t)
	version, err := Version(root)
	if err != nil {
		t.Fatal(err)
	}
	bin, err := Binary(context.Background(), execx.OS{}, root, Platform{OS: "linux", Arch: "amd64"}, version)
	if err != nil {
		t.Fatal(err)
	}
	data, err := Pack(root, bin, version)
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	got := map[string]int64{}
	var versionFile, fleetMD string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got[hdr.Name] = hdr.Mode
		body, _ := io.ReadAll(tr)
		switch hdr.Name {
		case "VERSION":
			versionFile = string(body)
		case "home/FLEET.md":
			fleetMD = string(body)
		case "bin/fleet":
			if !bytes.HasPrefix(body, []byte("\x7fELF")) {
				t.Error("bin/fleet is not a Linux ELF binary")
			}
		}
	}
	for _, want := range []string{"fleet.toml", "VERSION", "SOURCE", "home/manifest.toml", "home/FLEET.md", "home/instructions.md", "container/Containerfile", "bin/fleet"} {
		if _, ok := got[want]; !ok {
			t.Errorf("bundle missing %s", want)
		}
	}
	if got["bin/fleet"] != 0o755 || got["container/entrypoint.sh"] != 0o755 {
		t.Errorf("exec bits: bin %o entrypoint %o", got["bin/fleet"], got["container/entrypoint.sh"])
	}
	if versionFile != version+"\n" {
		t.Errorf("VERSION = %q", versionFile)
	}
	if !strings.Contains(fleetMD, "| worker |") {
		t.Error("FLEET.md not rendered from inventory")
	}
}

func TestInstalledSourceOnlyWhereTheCheckoutExists(t *testing.T) {
	bundleDir := t.TempDir()
	root := repoRoot(t)
	if err := os.WriteFile(filepath.Join(bundleDir, "SOURCE"), []byte(root+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if src, ok := InstalledSource(bundleDir); !ok || src != root {
		t.Fatalf("controller checkout not found: %q %v", src, ok)
	}
	if err := os.WriteFile(filepath.Join(bundleDir, "SOURCE"), []byte("/nonexistent/example/checkout\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := InstalledSource(bundleDir); ok {
		t.Fatal("a path that is not a checkout on this machine was accepted")
	}
	if _, ok := InstalledSource(t.TempDir()); ok {
		t.Fatal("a bundle without SOURCE was accepted")
	}
}

func TestInstalledVersion(t *testing.T) {
	dir := t.TempDir()
	if _, err := InstalledVersion(dir); err == nil || !strings.Contains(err.Error(), "fleet push") {
		t.Fatalf("err = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "VERSION"), []byte("abc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if v, err := InstalledVersion(dir); v != "abc" || err != nil {
		t.Fatalf("v = %q, %v", v, err)
	}
}
