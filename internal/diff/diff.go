// Package diff compares a pack against a live server directory.
package diff

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Scarsz/serverpack/internal/fetch"
	"github.com/Scarsz/serverpack/internal/importer"
	"github.com/Scarsz/serverpack/internal/pack"
	"github.com/Scarsz/serverpack/internal/render"
	"github.com/Scarsz/serverpack/internal/runtime"
	"github.com/Scarsz/serverpack/internal/srcbuild"
)

// Kind classifies a difference.
type Kind string

const (
	OnlyInPack   Kind = "+" // the image would add this file
	OnlyInLive   Kind = "-" // the live server has a file the pack doesn't
	Modified     Kind = "~"
	Incomparable Kind = "?"
)

// Entry is one difference.
type Entry struct {
	Kind Kind
	Path string
	Note string
}

func (e Entry) String() string {
	if e.Note != "" {
		return fmt.Sprintf("%s %s  (%s)", e.Kind, e.Path, e.Note)
	}
	return fmt.Sprintf("%s %s", e.Kind, e.Path)
}

// Options configure a diff.
type Options struct {
	Cache   *fetch.Cache
	Builder *srcbuild.Builder
	Lookup  func(string) (string, bool) // env for expanding templates; defaults to os.LookupEnv
}

// Diff renders p and compares it with liveDir. Volume paths, worlds and generated runtime files are ignored.
// Jars in jar directories are compared by content, so renamed jars (ViaVersion-5.1.jar vs ViaVersion.jar) match.
func Diff(ctx context.Context, p *pack.Pack, liveDir string, opts Options) ([]Entry, error) {
	if opts.Lookup == nil {
		opts.Lookup = os.LookupEnv
	}
	tmp, err := os.MkdirTemp("", "serverpack-diff-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	rt, err := render.Render(ctx, p, render.Options{Cache: opts.Cache, Builder: opts.Builder, JarsDir: tmp, ConfigDir: tmp})
	if err != nil {
		return nil, err
	}
	templated := map[string]bool{}
	for _, t := range rt.Templates {
		templated[t] = true
	}

	want, err := hashTree(tmp, func(rel string, isDir bool) bool {
		return rel == render.MetaDir
	})
	if err != nil {
		return nil, err
	}
	have, err := hashTree(liveDir, func(rel string, isDir bool) bool {
		if p.UnderVolume(rel) {
			return true
		}
		if isDir {
			return importer.IsRuntimeDir(rel) || importer.IsWorldDir(filepath.Join(liveDir, filepath.FromSlash(rel)))
		}
		return importer.IsRuntimeFile(rel)
	})
	if err != nil {
		return nil, err
	}

	isJar := func(rel string) bool { return p.IsVendoredJarPath(rel) }
	var out []Entry

	// Jars: compare by content per directory.
	wantJars, haveJars := jarsByDir(want, isJar), jarsByDir(have, isJar)
	for dir, wj := range wantJars {
		hj := haveJars[dir]
		for sum, rel := range wj {
			if _, ok := hj[sum]; ok {
				continue // same content; the file name doesn't matter to the server
			}
			out = append(out, Entry{Kind: OnlyInPack, Path: rel})
		}
	}
	for dir, hj := range haveJars {
		for sum, rel := range hj {
			if _, ok := wantJars[dir][sum]; !ok {
				out = append(out, Entry{Kind: OnlyInLive, Path: rel})
			}
		}
	}

	for rel, sum := range want {
		if isJar(rel) {
			continue
		}
		liveSum, ok := have[rel]
		if !ok {
			out = append(out, Entry{Kind: OnlyInPack, Path: rel})
			continue
		}
		if templated[rel] {
			data, err := os.ReadFile(filepath.Join(tmp, filepath.FromSlash(rel)))
			if err != nil {
				return nil, err
			}
			expanded, missing := runtime.Expand(string(data), opts.Lookup)
			if len(missing) > 0 {
				out = append(out, Entry{Kind: Incomparable, Path: rel, Note: "templated; set " + strings.Join(missing, ", ") + " to compare"})
				continue
			}
			sum = fetch.HashBytes([]byte(expanded))
		}
		if sum != liveSum {
			out = append(out, Entry{Kind: Modified, Path: rel})
		}
	}
	for rel := range have {
		if _, ok := want[rel]; !ok && !isJar(rel) {
			out = append(out, Entry{Kind: OnlyInLive, Path: rel})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Kind < out[j].Kind
	})
	return out, nil
}

func jarsByDir(tree map[string]string, isJar func(string) bool) map[string]map[string]string {
	out := map[string]map[string]string{}
	for rel, sum := range tree {
		if !isJar(rel) {
			continue
		}
		dir := path.Dir(rel)
		if out[dir] == nil {
			out[dir] = map[string]string{}
		}
		out[dir][sum] = rel
	}
	return out
}

// hashTree maps every regular file under root (slash paths) to its sha256.
func hashTree(root string, skip func(rel string, isDir bool) bool) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.WalkDir(root, func(abs string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, abs)
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if skip(rel, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		b, err := fetch.HashFile(abs)
		if err != nil {
			return err
		}
		out[rel] = b.SHA256
		return nil
	})
	return out, err
}
