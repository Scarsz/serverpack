// Package pack discovers server repos, layers servers over their bases and builds the merged /data tree.
package pack

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/Scarsz/serverpack/internal/descriptor"
)

const (
	ServersDir = "servers"
	BasesDir   = "bases"
	ServerJar  = "server.jar"
)

// Kind distinguishes servers from bases.
type Kind string

const (
	KindServer Kind = "server"
	KindBase   Kind = "base"
)

// Layer is one directory contributing to a pack (a base or the server itself).
type Layer struct {
	Name     string
	Kind     Kind
	Dir      string
	Manifest *Manifest
}

// File is a file in the merged /data tree.
type File struct {
	Rel   string // slash-separated path relative to /data
	Abs   string
	Layer *Layer
}

// Entry is an active jar descriptor: a descriptor file in a jar dir, or the [server] table of server.toml.
type Entry struct {
	File     *File // the file holding the descriptor (for the server: the owning layer's server.toml)
	Desc     *descriptor.Descriptor
	Target   string // slash path of the jar this descriptor produces
	IsServer bool
}

// Name is the descriptor's short name (file basename without .toml, or "server").
func (e *Entry) Name() string {
	if e.IsServer {
		return "server"
	}
	return strings.TrimSuffix(path.Base(e.File.Rel), ".toml")
}

// Table is the TOML table that holds the entry's pin.
func (e *Entry) Table() string {
	if e.IsServer {
		return ServerTable + "." + descriptor.ResolvedTable
	}
	return descriptor.ResolvedTable
}

// Where describes the entry's location for messages ("plugins/X.toml", "server.toml [server]").
func (e *Entry) Where() string {
	if e.IsServer {
		return ManifestFile + " [" + ServerTable + "]"
	}
	return e.File.Rel
}

// Pack is a fully layered server (or base).
type Pack struct {
	Root     string
	Name     string
	Self     *Layer   // the server/base this pack was loaded for
	Layers   []*Layer // bases first, Self last
	Manifest Manifest // merged
	Files    map[string]*File
	Entries  []*Entry // active descriptors: the server first, then by path
	Disabled []*File  // descriptors with disabled = true
}

// FindRoot walks up from start to the first directory containing servers/ or bases/.
func FindRoot(start string) (string, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		for _, sub := range []string{ServersDir, BasesDir} {
			if st, err := os.Stat(filepath.Join(dir, sub)); err == nil && st.IsDir() {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no serverpack repo found (looked for a %s/ or %s/ directory above %s)", ServersDir, BasesDir, start)
		}
		dir = parent
	}
}

// ListServers returns the names of all servers in the repo.
func ListServers(root string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, ServersDir))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			if _, err := os.Stat(filepath.Join(root, ServersDir, e.Name(), ManifestFile)); err == nil {
				names = append(names, e.Name())
			}
		}
	}
	return names, nil
}

// Dir returns the directory for a server or base name.
func Dir(root string, kind Kind, name string) string {
	if kind == KindBase {
		return filepath.Join(root, BasesDir, name)
	}
	return filepath.Join(root, ServersDir, name)
}

// LocateTarget resolves a name to a server or base. Names may be prefixed with "base:" or "server:".
func LocateTarget(root, name string) (Kind, string, error) {
	if n, ok := strings.CutPrefix(name, "base:"); ok {
		return KindBase, n, nil
	}
	if n, ok := strings.CutPrefix(name, "server:"); ok {
		return KindServer, n, nil
	}
	_, serr := os.Stat(filepath.Join(Dir(root, KindServer, name), ManifestFile))
	_, berr := os.Stat(filepath.Join(Dir(root, KindBase, name), ManifestFile))
	switch {
	case serr == nil && berr == nil:
		return "", "", fmt.Errorf("%q is both a server and a base; use server:%s or base:%s", name, name, name)
	case serr == nil:
		return KindServer, name, nil
	case berr == nil:
		return KindBase, name, nil
	}
	return "", "", fmt.Errorf("no server or base named %q (expected %s)", name, filepath.Join(ServersDir, name, ManifestFile))
}

// Load loads a server by name.
func Load(root, name string) (*Pack, error) {
	return LoadKind(root, KindServer, name)
}

// LoadKind loads a server or base and layers it over everything it extends.
func LoadKind(root string, kind Kind, name string) (*Pack, error) {
	self, err := loadLayer(root, kind, name)
	if err != nil {
		return nil, err
	}
	var layers []*Layer
	seen := map[string]bool{}
	var visit func(l *Layer, stack []string) error
	visit = func(l *Layer, stack []string) error {
		for _, parent := range l.Manifest.Extends {
			if slices.Contains(stack, parent) {
				return fmt.Errorf("extends cycle: %s -> %s", strings.Join(stack, " -> "), parent)
			}
			if seen[parent] {
				continue
			}
			pl, err := loadLayer(root, KindBase, parent)
			if err != nil {
				return fmt.Errorf("%s extends %q: %w", l.Name, parent, err)
			}
			if err := visit(pl, append(stack, parent)); err != nil {
				return err
			}
			seen[parent] = true
			layers = append(layers, pl)
		}
		return nil
	}
	if err := visit(self, []string{string(kind) + ":" + name}); err != nil {
		return nil, err
	}
	layers = append(layers, self)

	p := &Pack{Root: root, Name: name, Self: self, Layers: layers, Files: map[string]*File{}}
	var serverLayer *Layer
	for _, l := range layers {
		p.Manifest.merge(l.Manifest)
		if l.Manifest.Server != nil {
			// [server] is replaced as a whole by the top-most layer that defines it.
			p.Manifest.Server, serverLayer = l.Manifest.Server, l
		}
	}
	if s := p.Manifest.Server; s != nil && !s.Disabled {
		p.Entries = append(p.Entries, &Entry{
			File:     &File{Rel: ManifestFile, Abs: filepath.Join(serverLayer.Dir, ManifestFile), Layer: serverLayer},
			Desc:     s,
			Target:   targetFor(ManifestFile, s, true),
			IsServer: true,
		})
	}
	if self.Manifest.Name == "" {
		p.Manifest.Name = name
	}
	p.Name = p.Manifest.Name
	if p.Manifest.JarDirs == nil {
		p.Manifest.JarDirs = DefaultJarDirs
	}
	for i, v := range p.Manifest.Volumes.Paths {
		p.Manifest.Volumes.Paths[i] = strings.Trim(path.Clean(filepath.ToSlash(v)), "/")
	}
	if err := p.buildTree(); err != nil {
		return nil, err
	}
	return p, nil
}

func loadLayer(root string, kind Kind, name string) (*Layer, error) {
	dir := Dir(root, kind, name)
	m, err := LoadManifest(filepath.Join(dir, ManifestFile))
	if err != nil {
		return nil, err
	}
	return &Layer{Name: name, Kind: kind, Dir: dir, Manifest: m}, nil
}

// ignored reports whether a path in a layer dir is not part of /data.
func ignored(rel string, isDir bool) bool {
	base := path.Base(rel)
	if isDir {
		return base == ".git" || base == ".serverpack"
	}
	return rel == ManifestFile || base == ".gitignore" || base == ".gitkeep" || base == ".gitattributes"
}

func (p *Pack) buildTree() error {
	for _, l := range p.Layers {
		err := filepath.WalkDir(l.Dir, func(abs string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(l.Dir, abs)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if rel == "." {
				return nil
			}
			if ignored(rel, d.IsDir()) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				return nil
			}
			p.Files[rel] = &File{Rel: rel, Abs: abs, Layer: l}
			return nil
		})
		if err != nil {
			return err
		}
	}

	var errs []error
	for _, rel := range p.SortedPaths() {
		f := p.Files[rel]
		if !p.IsDescriptorPath(rel) {
			continue
		}
		d, err := descriptor.Load(f.Abs)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if d.Disabled {
			p.Disabled = append(p.Disabled, f)
			continue
		}
		p.Entries = append(p.Entries, &Entry{File: f, Desc: d, Target: targetFor(rel, d, false)})
	}
	return errors.Join(errs...)
}

func targetFor(rel string, d *descriptor.Descriptor, isServer bool) string {
	if isServer {
		if d.Filename != "" {
			return d.Filename
		}
		return ServerJar
	}
	name := d.Filename
	if name == "" {
		name = strings.TrimSuffix(path.Base(rel), ".toml") + ".jar"
	}
	return path.Join(path.Dir(rel), name)
}

// IsDescriptorPath reports whether rel is a jar descriptor location (a .toml directly inside a jar dir).
func (p *Pack) IsDescriptorPath(rel string) bool {
	return strings.HasSuffix(rel, ".toml") && p.isJarDir(path.Dir(rel))
}

// IsVendoredJarPath reports whether rel is a jar placed directly in a jar dir or the root.
func (p *Pack) IsVendoredJarPath(rel string) bool {
	if !strings.HasSuffix(rel, ".jar") {
		return false
	}
	dir := path.Dir(rel)
	return dir == "." || p.isJarDir(dir)
}

func (p *Pack) isJarDir(dir string) bool {
	for _, jd := range p.Manifest.JarDirs {
		if strings.Trim(path.Clean(filepath.ToSlash(jd)), "/") == dir {
			return true
		}
	}
	return false
}

// SortedPaths returns all tree paths in lexical order.
func (p *Pack) SortedPaths() []string {
	paths := make([]string, 0, len(p.Files))
	for rel := range p.Files {
		paths = append(paths, rel)
	}
	sort.Strings(paths)
	return paths
}

// Server returns the server.jar descriptor entry, if any.
func (p *Pack) Server() *Entry {
	for _, e := range p.Entries {
		if e.IsServer {
			return e
		}
	}
	return nil
}

// UnderVolume reports whether rel is inside (or is) a declared volume path.
func (p *Pack) UnderVolume(rel string) bool {
	for _, v := range p.Manifest.Volumes.Paths {
		if rel == v || strings.HasPrefix(rel, v+"/") {
			return true
		}
	}
	return false
}

// LocalPath returns the filesystem path of the jar a local descriptor points at.
func (e *Entry) LocalPath() string {
	p := e.Desc.Path
	if p == "" {
		p = e.Name() + ".jar"
		if e.IsServer {
			p = ServerJar
		}
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(filepath.Dir(e.File.Abs), filepath.FromSlash(p))
}

// ProjectDir returns the project directory of a path-based build descriptor (relative to the descriptor file).
func (e *Entry) ProjectDir() string {
	p := filepath.FromSlash(e.Desc.Path)
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(filepath.Dir(e.File.Abs), p)
}

// FindEntry finds an active descriptor by short name or relative path.
func (p *Pack) FindEntry(name string) *Entry {
	for _, e := range p.Entries {
		if e.Name() == name || (!e.IsServer && e.File.Rel == name) {
			return e
		}
	}
	return nil
}
