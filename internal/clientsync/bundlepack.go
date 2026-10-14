// The bundle archive `sync bundle` writes: a gzip tar of the rig spec and the agent directories the rig root
// holds, with a manifest naming every file and its hash.
//
// It is built here, not by the OpenRig tool, so a machine without OpenRig can ship a seat. The archive is
// DETERMINISTIC - sorted entries, a zero mtime, fixed modes - so the same pin and the same rig root always
// produce the same bytes, and a bundle's hash is a fact about its contents rather than about the day it was
// built. `offline-install` reads the unpacked directory (`rig.yaml` and `agents/<agent>/`), which is the
// layout this archive carries.
package clientsync

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// bundleManifestName is the first entry of every bundle.
const bundleManifestName = "manifest.json"

// bundleManifest says what the bundle is and what it holds, so a reader can verify the contents without
// trusting the archive's own framing.
type bundleManifest struct {
	Name    string               `json:"name"`
	Version string               `json:"version"`
	Notes   string               `json:"notes"`
	Files   []bundleManifestFile `json:"files"`
}

type bundleManifestFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// bundleFile is one file that goes into the archive.
type bundleFile struct {
	path    string
	content []byte
}

// bundleSpec is what a bundle is built from.
type bundleSpec struct {
	name, version, notes string
	rigYAML, rigRoot     string
}

// bundleCollect reads the files a bundle carries: the rig spec as `rig.yaml`, and every regular file under
// `<rig root>/agents`. A symlink is skipped, never followed, so a bundle cannot be made to carry a file from
// outside the rig root.
func bundleCollect(spec bundleSpec) ([]bundleFile, error) {
	yamlBytes, err := os.ReadFile(spec.rigYAML)
	if err != nil {
		return nil, fmt.Errorf("cannot read the rig spec: %w", err)
	}
	files := []bundleFile{{path: "rig.yaml", content: yamlBytes}}

	agents := filepath.Join(spec.rigRoot, "agents")
	if info, err := os.Stat(agents); err != nil || !info.IsDir() {
		return files, nil
	}
	walkErr := filepath.WalkDir(agents, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		relative, err := filepath.Rel(spec.rigRoot, path)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files = append(files, bundleFile{path: filepath.ToSlash(relative), content: content})
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("cannot read the rig root's agents: %w", walkErr)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	return files, nil
}

// bundlePack writes the archive to path, through a temporary file renamed into place so a failed build
// leaves no half-written bundle under the name a caller will read.
func bundlePack(spec bundleSpec, path string) error {
	files, err := bundleCollect(spec)
	if err != nil {
		return err
	}
	manifest := bundleManifest{Name: spec.name, Version: spec.version, Notes: spec.notes,
		Files: make([]bundleManifestFile, 0, len(files))}
	for _, file := range files {
		sum := sha256.Sum256(file.content)
		manifest.Files = append(manifest.Files,
			bundleManifestFile{Path: file.path, Size: int64(len(file.content)), SHA256: hex.EncodeToString(sum[:])})
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}

	temp, err := os.CreateTemp(filepath.Dir(path), ".bundle-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())

	gz := gzip.NewWriter(temp)
	archive := tar.NewWriter(gz)
	for _, file := range append([]bundleFile{{path: bundleManifestName, content: manifestBytes}}, files...) {
		header := &tar.Header{Name: file.path, Mode: 0o644, Size: int64(len(file.content)), ModTime: time.Unix(0, 0)}
		if err := archive.WriteHeader(header); err != nil {
			return err
		}
		if _, err := archive.Write(file.content); err != nil {
			return err
		}
	}
	if err := archive.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}
