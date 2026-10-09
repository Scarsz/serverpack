package pack

import (
	"bytes"
	"fmt"
	"os"
	"slices"

	"github.com/pelletier/go-toml/v2"

	"github.com/Scarsz/serverpack/internal/descriptor"
)

// ManifestFile is the per-server / per-base manifest filename.
const ManifestFile = "server.toml"

// ServerTable is the manifest table describing server.jar; its pin lives in [server.resolved].
const ServerTable = "server"

// DefaultJarDirs are the directories scanned for jar descriptors when jar_dirs is unset.
var DefaultJarDirs = []string{"plugins", "mods"}

// Manifest is the content of server.toml. Nil slices mean "not set in this layer".
type Manifest struct {
	Name      string                 `toml:"name"`
	Extends   []string               `toml:"extends"`
	Server    *descriptor.Descriptor `toml:"server"`
	JarDirs   []string               `toml:"jar_dirs"`
	Runtime   Runtime                `toml:"runtime"`
	Build     Build                  `toml:"build"`
	Volumes   Volumes                `toml:"volumes"`
	Templates Templates              `toml:"templates"`
}

type Runtime struct {
	Image          string   `toml:"image"`
	Java           string   `toml:"java"`
	Memory         Memory   `toml:"memory"`
	JVMFlags       []string `toml:"jvm_flags"`
	JVMFlagsAppend []string `toml:"jvm_flags_append"`
	Args           []string `toml:"args"`
}

type Memory struct {
	Min string `toml:"min"`
	Max string `toml:"max"`
}

type Build struct {
	Images          []string `toml:"images"`
	ImagesAppend    []string `toml:"images_append"`
	Tags            []string `toml:"tags"`
	TagsAppend      []string `toml:"tags_append"`
	Platforms       []string `toml:"platforms"`
	ServerpackImage string   `toml:"serverpack_image"`
}

type Volumes struct {
	Paths []string `toml:"paths"`
}

type Templates struct {
	Include []string `toml:"include"`
}

// LoadManifest parses a server.toml strictly.
func LoadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		if se, ok := err.(*toml.StrictMissingError); ok {
			return nil, fmt.Errorf("%s: %s", path, se.String())
		}
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if m.Server != nil && !m.Server.Disabled && m.Server.Source == "" {
		return nil, fmt.Errorf("%s: [server] is missing required field \"source\"", path)
	}
	return &m, nil
}

// merge layers top over m following the documented rules.
func (m *Manifest) merge(top *Manifest) {
	if top.Name != "" {
		m.Name = top.Name
	}
	m.JarDirs = replace(m.JarDirs, top.JarDirs)

	r, tr := &m.Runtime, &top.Runtime
	r.Image = scalar(r.Image, tr.Image)
	r.Java = scalar(r.Java, tr.Java)
	r.Memory.Min = scalar(r.Memory.Min, tr.Memory.Min)
	r.Memory.Max = scalar(r.Memory.Max, tr.Memory.Max)
	r.JVMFlags = append(replace(r.JVMFlags, tr.JVMFlags), tr.JVMFlagsAppend...)
	r.Args = replace(r.Args, tr.Args)

	b, tb := &m.Build, &top.Build
	b.Images = append(replace(b.Images, tb.Images), tb.ImagesAppend...)
	b.Tags = append(replace(b.Tags, tb.Tags), tb.TagsAppend...)
	b.Platforms = replace(b.Platforms, tb.Platforms)
	b.ServerpackImage = scalar(b.ServerpackImage, tb.ServerpackImage)

	m.Volumes.Paths = union(m.Volumes.Paths, top.Volumes.Paths)
	m.Templates.Include = union(m.Templates.Include, top.Templates.Include)
}

func scalar(cur, top string) string {
	if top != "" {
		return top
	}
	return cur
}

func replace(cur, top []string) []string {
	if top != nil {
		return slices.Clone(top)
	}
	return cur
}

func union(cur, top []string) []string {
	out := slices.Clone(cur)
	for _, v := range top {
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}
