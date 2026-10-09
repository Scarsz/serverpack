// Package render assembles a pack's /data tree: configs from the layered overlay and jars from the cache.
package render

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/Scarsz/serverpack/internal/fetch"
	"github.com/Scarsz/serverpack/internal/pack"
	"github.com/Scarsz/serverpack/internal/source"
	"github.com/Scarsz/serverpack/internal/srcbuild"
)

// MetaDir holds serverpack's own files inside /data.
const MetaDir = ".serverpack"

// RuntimeFile is the path (relative to /data) of the runtime config consumed by `serverpack run`.
const RuntimeFile = MetaDir + "/runtime.json"

// TemplatesDir keeps pristine copies of templated files so every container start renders from scratch.
const TemplatesDir = MetaDir + "/templates"

// Runtime is written to /data/.serverpack/runtime.json.
type Runtime struct {
	Name      string   `json:"name"`
	MCVersion string   `json:"mc_version,omitempty"`
	Java      string   `json:"java"`
	JVMArgs   []string `json:"jvm_args"`
	Jar       string   `json:"jar"`
	Args      []string `json:"args"`
	Templates []string `json:"templates"`
	Volumes   []string `json:"volumes"`
}

// Command returns the full java command line.
func (r *Runtime) Command() []string {
	cmd := append([]string{r.Java}, r.JVMArgs...)
	cmd = append(cmd, "-jar", r.Jar)
	return append(cmd, r.Args...)
}

// Options configure a render.
type Options struct {
	Cache     *fetch.Cache
	JarsDir   string            // output root for jars
	ConfigDir string            // output root for everything else (may equal JarsDir)
	Builder   *srcbuild.Builder // builds source = "build" jars
	Warn      func(format string, args ...any)
}

// within reports whether p is dir or inside it.
func within(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// Validate reports descriptors that can't be rendered (unlocked, stale, missing local jars).
func Validate(p *pack.Pack) error {
	return errors.Join(Problems(p)...)
}

// Problems lists every reason p can't be rendered.
func Problems(p *pack.Pack) []error {
	var errs []error
	if p.Server() == nil {
		errs = append(errs, fmt.Errorf("%s: no [%s] table in %s (it describes server.jar)", p.Name, pack.ServerTable, pack.ManifestFile))
	}
	targets := map[string]string{}
	for _, e := range p.Entries {
		if prev, dup := targets[e.Target]; dup {
			errs = append(errs, fmt.Errorf("%s: %s and %s both produce %s", p.Name, prev, e.Where(), e.Target))
		}
		targets[e.Target] = e.Where()
		if p.UnderVolume(e.Target) {
			errs = append(errs, fmt.Errorf("%s: %s produces %s, which is inside a volume path", p.Name, e.Where(), e.Target))
		}
		if _, err := source.Get(e.Desc.Source); err != nil {
			errs = append(errs, fmt.Errorf("%s: %s: %w", p.Name, e.Where(), err))
			continue
		}
		if e.Desc.Source == "local" {
			if _, err := os.Stat(e.LocalPath()); err != nil {
				errs = append(errs, fmt.Errorf("%s: %s: local jar missing: %w", p.Name, e.Where(), err))
			}
			continue
		}
		if e.Desc.Source == "build" {
			if err := srcbuild.ValidateSpec(e.Desc); err != nil {
				errs = append(errs, fmt.Errorf("%s: %s: %w", p.Name, e.Where(), err))
				continue
			}
			if e.Desc.Git == "" {
				dir := e.ProjectDir()
				if _, err := srcbuild.DetectTool(dir, e.Desc.Tool); err != nil {
					errs = append(errs, fmt.Errorf("%s: %s: %w", p.Name, e.Where(), err))
				}
				for _, l := range p.Layers {
					if within(l.Dir, dir) {
						errs = append(errs, fmt.Errorf("%s: %s: project %s is inside %s %s, so its sources would be copied into the image; move it outside servers/ and bases/",
							p.Name, e.Where(), dir, l.Kind, l.Name))
					}
				}
				continue
			}
		}
		switch {
		case e.Desc.Resolved == nil:
			errs = append(errs, fmt.Errorf("%s: %s is not locked; run `serverpack lock %s`", p.Name, e.Where(), p.Self.Name))
		case e.Desc.Stale():
			errs = append(errs, fmt.Errorf("%s: %s changed since it was locked; run `serverpack lock %s`", p.Name, e.Where(), p.Self.Name))
		case e.Desc.Source == "build" && e.Desc.Resolved.Revision == "":
			errs = append(errs, fmt.Errorf("%s: %s has no pinned revision; run `serverpack update %s %s`", p.Name, e.Where(), p.Self.Name, e.Name()))
		}
	}
	return errs
}

// IsTemplate reports whether rel matches the pack's template globs.
func IsTemplate(p *pack.Pack, rel string) bool {
	for _, pat := range p.Manifest.Templates.Include {
		if MatchGlob(pat, rel) {
			return true
		}
	}
	return false
}

// MatchGlob matches a /data-relative slash path against a ** glob.
func MatchGlob(pat, rel string) bool {
	ok, _ := doublestar.Match(strings.TrimPrefix(filepath.ToSlash(pat), "/"), rel)
	return ok
}

// ConfigFiles returns the overlay files that end up in /data (not descriptors, vendored jars or volume paths).
func ConfigFiles(p *pack.Pack, warn func(string, ...any)) []*pack.File {
	skip := map[string]bool{}
	for _, e := range p.Entries {
		skip[e.File.Rel] = true
	}
	for _, f := range p.Disabled {
		skip[f.Rel] = true
	}
	localJars := map[string]bool{}
	for _, e := range p.Entries {
		if e.Desc.Source == "local" {
			localJars[filepath.Clean(e.LocalPath())] = true
		}
	}
	var out []*pack.File
	for _, rel := range p.SortedPaths() {
		f := p.Files[rel]
		switch {
		case skip[rel]:
		case p.IsVendoredJarPath(rel):
			if !localJars[filepath.Clean(f.Abs)] && warn != nil {
				warn("%s: %s is a jar without a descriptor and is not included; add a source = \"local\" descriptor for it", p.Name, rel)
			}
		case p.UnderVolume(rel):
			if warn != nil {
				warn("%s: %s is inside a volume path and is not included in the image", p.Name, rel)
			}
		default:
			out = append(out, f)
		}
	}
	return out
}

// Render writes the pack's /data tree and returns the runtime config it wrote.
func Render(ctx context.Context, p *pack.Pack, opts Options) (*Runtime, error) {
	if err := Validate(p); err != nil {
		return nil, err
	}
	if opts.Warn == nil {
		opts.Warn = func(string, ...any) {}
	}

	rt := &Runtime{
		Name:    p.Name,
		Java:    p.Manifest.Runtime.Java,
		Args:    p.Manifest.Runtime.Args,
		Volumes: p.Manifest.Volumes.Paths,
	}
	if rt.Java == "" {
		rt.Java = "java"
	}
	if rt.Args == nil {
		rt.Args = []string{}
	}
	if rt.Volumes == nil {
		rt.Volumes = []string{}
	}
	rt.Templates = []string{}
	if m := p.Manifest.Runtime.Memory.Min; m != "" {
		rt.JVMArgs = append(rt.JVMArgs, "-Xms"+m)
	}
	if m := p.Manifest.Runtime.Memory.Max; m != "" {
		rt.JVMArgs = append(rt.JVMArgs, "-Xmx"+m)
	}
	rt.JVMArgs = append(rt.JVMArgs, p.Manifest.Runtime.JVMFlags...)
	srv := p.Server()
	rt.Jar = srv.Target
	rt.MCVersion, _ = source.ServerContext(srv.Desc)

	for _, f := range ConfigFiles(p, opts.Warn) {
		if err := copyFile(f.Abs, filepath.Join(opts.ConfigDir, filepath.FromSlash(f.Rel))); err != nil {
			return nil, err
		}
		if IsTemplate(p, f.Rel) {
			if err := copyFile(f.Abs, filepath.Join(opts.ConfigDir, filepath.FromSlash(TemplatesDir), filepath.FromSlash(f.Rel))); err != nil {
				return nil, err
			}
			rt.Templates = append(rt.Templates, f.Rel)
		}
	}
	for _, pat := range p.Manifest.Templates.Include {
		if !anyMatch(pat, rt.Templates) {
			opts.Warn("%s: template pattern %q matches no files", p.Name, pat)
		}
	}

	for _, e := range p.Entries {
		dst := filepath.Join(opts.JarsDir, filepath.FromSlash(e.Target))
		var src string
		if e.Desc.Source == "local" {
			src = e.LocalPath()
			if want := e.Desc.SHA256; want != "" {
				b, err := fetch.HashFile(src)
				if err != nil {
					return nil, err
				}
				if !strings.EqualFold(b.SHA256, want) {
					return nil, fmt.Errorf("%s: %s: sha256 mismatch: expected %s, got %s", p.Name, e.Where(), want, b.SHA256)
				}
			}
		} else if e.Desc.Source == "build" {
			if opts.Builder == nil {
				return nil, fmt.Errorf("%s: %s: building from source is not available here", p.Name, e.Where())
			}
			jar, err := opts.Builder.Jar(ctx, srcbuild.SpecFor(p.Name+": "+e.Where(), e.Desc, e.ProjectDir()))
			if err != nil {
				return nil, err
			}
			src = jar
		} else {
			r := e.Desc.Resolved
			blob, err := opts.Cache.Fetch(ctx, r.URL, fetch.Expect{SHA256: r.SHA256, Size: r.Size})
			if err != nil {
				if strings.Contains(err.Error(), "mismatch") {
					err = fmt.Errorf("%w; the upstream file changed since it was locked, run `serverpack update %s %s`", err, p.Self.Name, e.Name())
				}
				return nil, fmt.Errorf("%s: %s: %w", p.Name, e.Where(), err)
			}
			src = blob.Path
		}
		if err := copyFile(src, dst); err != nil {
			return nil, err
		}
	}

	data, err := json.MarshalIndent(rt, "", "  ")
	if err != nil {
		return nil, err
	}
	rtPath := filepath.Join(opts.ConfigDir, filepath.FromSlash(RuntimeFile))
	if err := os.MkdirAll(filepath.Dir(rtPath), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(rtPath, append(data, '\n'), 0o644); err != nil {
		return nil, err
	}
	return rt, nil
}

func anyMatch(pat string, rels []string) bool {
	for _, rel := range rels {
		if MatchGlob(pat, rel) {
			return true
		}
	}
	return false
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if st.Mode()&0o111 != 0 {
		mode = 0o755
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
