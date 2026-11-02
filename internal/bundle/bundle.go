// Package bundle packs what a node needs (inventory, shared layer, container
// recipe, fleet binary) into one tar.gz that push streams over SSH.
package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/vickingur/fleet-orchestrator/internal/docs"
	"github.com/vickingur/fleet-orchestrator/internal/execx"
	"github.com/vickingur/fleet-orchestrator/internal/inventory"
	"github.com/vickingur/fleet-orchestrator/internal/syncer"
)

// ModulePath identifies a fleet source checkout.
const ModulePath = "github.com/vickingur/fleet-orchestrator"

// FindSource walks up from dir to the fleet source checkout.
func FindSource(dir string) (string, error) {
	for d := dir; ; d = filepath.Dir(d) {
		mod, err := os.ReadFile(filepath.Join(d, "go.mod"))
		if err == nil && strings.HasPrefix(string(mod), "module "+ModulePath+"\n") {
			if _, err := os.Stat(filepath.Join(d, "fleet.toml")); err == nil {
				return d, nil
			}
		}
		if filepath.Dir(d) == d {
			return "", errors.New("not inside the fleet source checkout; run from the repo or set FLEET_SRC")
		}
	}
}

// LoadInventory reads fleet.toml from a source checkout or installed bundle.
func LoadInventory(root string) (*inventory.Inventory, error) {
	data, err := os.ReadFile(filepath.Join(root, "fleet.toml"))
	if err != nil {
		return nil, err
	}
	return inventory.Parse(data)
}

// Platform is a Go GOOS/GOARCH pair.
type Platform struct{ OS, Arch string }

func (p Platform) String() string { return p.OS + "/" + p.Arch }

// ParsePlatform maps `uname -sm` output to a Go platform.
func ParsePlatform(unameSM string) (Platform, error) {
	fields := strings.Fields(unameSM)
	if len(fields) != 2 {
		return Platform{}, fmt.Errorf("unexpected uname output %q", unameSM)
	}
	osName := map[string]string{"Darwin": "darwin", "Linux": "linux"}[fields[0]]
	arch := map[string]string{"x86_64": "amd64", "amd64": "amd64", "arm64": "arm64", "aarch64": "arm64"}[fields[1]]
	if osName == "" || arch == "" {
		return Platform{}, fmt.Errorf("unsupported platform %q", unameSM)
	}
	return Platform{OS: osName, Arch: arch}, nil
}

// versioned lists what determines a bundle's content version.
var versioned = []string{"fleet.toml", "go.mod", "go.sum", "home", "container", "cmd", "internal"}

// Version is a content hash of everything that ends up in a bundle, so two
// nodes with the same version are guaranteed to run the same layer and binary.
// Test files are excluded: changing a test changes nothing a node runs.
func Version(src string) (string, error) {
	files, err := listFiles(src, versioned)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, rel := range files {
		if strings.HasSuffix(rel, "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(src, rel))
		if err != nil {
			return "", err
		}
		h.Write(fmt.Appendf(nil, "%s\x00%d\x00", rel, len(data)))
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil))[:12], nil
}

func listFiles(src string, roots []string) ([]string, error) {
	var files []string
	for _, root := range roots {
		err := filepath.WalkDir(filepath.Join(src, root), func(p string, de fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if syncer.IsMachineState(de.Name()) {
				if de.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if de.Type().IsRegular() {
				rel, _ := filepath.Rel(src, p)
				files = append(files, filepath.ToSlash(rel))
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(files)
	return files, nil
}

// Binary cross-compiles fleet for p into the source's dist directory.
func Binary(ctx context.Context, r execx.Runner, src string, p Platform, version string) (string, error) {
	out := filepath.Join(src, "dist", "fleet-"+p.OS+"-"+p.Arch)
	env := append(os.Environ(), "GOOS="+p.OS, "GOARCH="+p.Arch, "CGO_ENABLED=0")
	var stderr bytes.Buffer
	res, err := r.Run(ctx, execx.Cmd{
		Name:    "go",
		Args:    []string{"build", "-C", src, "-trimpath", "-ldflags", "-s -w -X main.version=" + version, "-o", out, "./cmd/fleet"},
		Env:     env,
		Stderr:  &stderr,
		Timeout: 5 * time.Minute,
	})
	if err != nil {
		return "", fmt.Errorf("build fleet for %s: %w", p, err)
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("build fleet for %s: %s", p, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// Pack builds the tar.gz for a node: fleet.toml, VERSION, home/ with a freshly
// rendered FLEET.md, container/, and bin/fleet.
func Pack(src, binary, version string) ([]byte, error) {
	inv, err := LoadInventory(src)
	if err != nil {
		return nil, err
	}
	files, err := listFiles(src, []string{"home", "container"})
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(name string, data []byte, mode int64) error {
		hdr := &tar.Header{Name: name, Mode: mode, Size: int64(len(data)), ModTime: time.Unix(0, 0), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}
	fleetTOML, err := os.ReadFile(filepath.Join(src, "fleet.toml"))
	if err != nil {
		return nil, err
	}
	bin, err := os.ReadFile(binary)
	if err != nil {
		return nil, err
	}
	entries := []struct {
		name string
		data []byte
		mode int64
	}{
		{"fleet.toml", fleetTOML, 0o644},
		{"VERSION", []byte(version + "\n"), 0o644},
		// SOURCE lets the controller find its checkout from any directory; on
		// other machines the path does not exist, so they never think they are it.
		{"SOURCE", []byte(src + "\n"), 0o644},
		{"home/FLEET.md", docs.Render(inv), 0o644},
		{"bin/fleet", bin, 0o755},
	}
	for _, e := range entries {
		if err := add(e.name, e.data, e.mode); err != nil {
			return nil, err
		}
	}
	for _, rel := range files {
		if rel == "home/FLEET.md" {
			continue
		}
		info, err := os.Stat(filepath.Join(src, rel))
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(filepath.Join(src, rel))
		if err != nil {
			return nil, err
		}
		mode := int64(0o644)
		if info.Mode()&0o111 != 0 {
			mode = 0o755
		}
		if err := add(rel, data, mode); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// InstalledSource returns the source checkout recorded in an installed bundle,
// if that checkout exists on this machine.
func InstalledSource(bundleDir string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(bundleDir, "SOURCE"))
	if err != nil {
		return "", false
	}
	src := strings.TrimSpace(string(data))
	if found, err := FindSource(src); err != nil || found != src {
		return "", false
	}
	return src, true
}

// InstalledVersion reads the VERSION file of an installed bundle.
func InstalledVersion(bundleDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(bundleDir, "VERSION"))
	if err != nil {
		return "", fmt.Errorf("no installed bundle at %s; push one from the controller: fleet push <node>", bundleDir)
	}
	return strings.TrimSpace(string(data)), nil
}
