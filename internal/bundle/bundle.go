// Package bundle describes a dependency-closure bundle: one package plus every
// Frappe app it transitively requires, in install order.
//
// The types live here rather than in package cmd so that the OCI registry layer
// can pack and unpack a bundle without importing the command tree, which would
// be a cycle. cmd/bundle.go aliases them, so the on-disk format is unchanged and
// every existing bundle directory stays readable.
package bundle

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ManifestName is the file that marks a directory as a dependency-closure
// bundle and lists its packages in install order.
const ManifestName = "fpm-bundle.json"

// Entry is one package in a bundle.
type Entry struct {
	Org     string `json:"org"`
	App     string `json:"app"`
	Version string `json:"version"`
	// File is the package's filename inside the bundle directory.
	File       string `json:"file"`
	RequiredBy string `json:"required_by,omitempty"`
	CommitSHA  string `json:"commit_sha,omitempty"`
	// ProvidedBy is "bench" for a requirement satisfied by an app already in the
	// bench the bundle was made against: it is listed for completeness but no
	// package file is shipped, and the target bench must have it too. This is how
	// a base-image app (erpnext, say) is recorded without being carried.
	ProvidedBy string `json:"provided_by,omitempty"`
}

// Identifier renders org/app==version.
func (e Entry) Identifier() string { return e.Org + "/" + e.App + "==" + e.Version }

// Shipped reports whether this entry carries a package file. An entry the bench
// provides has no file and must never be looked for on disk or in a registry.
func (e Entry) Shipped() bool { return e.ProvidedBy != "bench" && e.File != "" }

// Manifest describes a bundle: the package it was made for and every package an
// offline bench needs, each exactly once, deepest dependency first.
type Manifest struct {
	Root         Entry   `json:"root"`
	InstallOrder []Entry `json:"install_order"`
	CreatedBy    string  `json:"created_by"`
}

// Shipped returns the entries that carry a package file, in install order.
func (m *Manifest) Shipped() []Entry {
	var out []Entry
	for _, e := range m.InstallOrder {
		if e.Shipped() {
			out = append(out, e)
		}
	}
	return out
}

// Read loads the manifest from a bundle directory.
func Read(dir string) (*Manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, ManifestName))
	if err != nil {
		return nil, fmt.Errorf("not a bundle directory (%s missing): %w", ManifestName, err)
	}
	return Parse(data)
}

// Parse decodes a manifest and checks the invariants the installer relies on.
func Parse(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("invalid %s: %w", ManifestName, err)
	}
	if len(m.InstallOrder) == 0 {
		return nil, fmt.Errorf("invalid %s: install_order is empty", ManifestName)
	}
	for i, e := range m.InstallOrder {
		if e.App == "" || e.Version == "" {
			return nil, fmt.Errorf("invalid %s: entry %d has no app or version", ManifestName, i)
		}
		if e.Shipped() && filepath.Base(e.File) != e.File {
			// A bundle is unpacked into a directory the caller chose; an entry whose
			// file escapes it (../, or an absolute path) would write outside.
			return nil, fmt.Errorf("invalid %s: entry %s has a non-local file %q", ManifestName, e.Identifier(), e.File)
		}
	}
	return &m, nil
}

// Write serialises the manifest into a bundle directory.
func Write(dir string, m *Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, ManifestName), data, 0o644)
}
