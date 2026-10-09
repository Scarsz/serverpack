// Package importer bootstraps a server definition from an existing live server directory.
package importer

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/Scarsz/serverpack/internal/descriptor"
	"github.com/Scarsz/serverpack/internal/fetch"
	"github.com/Scarsz/serverpack/internal/httpx"
	"github.com/Scarsz/serverpack/internal/pack"
	"github.com/Scarsz/serverpack/internal/source"
)

// Options configure an import.
type Options struct {
	Root          string
	Name          string
	LiveDir       string
	Force         bool
	Image         string
	MemoryMin     string
	MemoryMax     string
	JVMFlags      []string
	Args          []string
	MaxConfigSize int64
	HTTP          *httpx.Client
	Cache         *fetch.Cache
	Modrinth      *source.Modrinth
	PaperMC       *source.PaperMC
	Offline       bool // don't try to identify jars online
}

// Report summarizes what an import did.
type Report struct {
	Dir        string
	Identified []string // "plugins/ViaVersion.toml (modrinth viaversion 5.12.1)"
	Vendored   []string
	Server     string
	Skipped    []string // large / runtime files not imported, with reason
	Volumes    []string // suggested volume paths written to server.toml
	Worlds     []string
	Configs    int
}

type jarFile struct {
	rel  string // path in the live dir
	dir  string // jar dir ("plugins"), or "." for the server jar
	abs  string
	blob *fetch.Blob
	info jarInfo
}

// Import creates servers/<name> from opts.LiveDir.
func Import(ctx context.Context, opts Options) (*Report, error) {
	if opts.MaxConfigSize == 0 {
		opts.MaxConfigSize = 4 << 20
	}
	dst := pack.Dir(opts.Root, pack.KindServer, opts.Name)
	if _, err := os.Stat(dst); err == nil && !opts.Force {
		return nil, fmt.Errorf("%s already exists (use --force to import into it anyway)", dst)
	}
	live, err := filepath.Abs(opts.LiveDir)
	if err != nil {
		return nil, err
	}
	if st, err := os.Stat(live); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", opts.LiveDir)
	}

	rep := &Report{Dir: dst}
	volumes := []string{"logs"}
	addVolume := func(v string) {
		if !slices.Contains(volumes, v) {
			volumes = append(volumes, v)
		}
	}
	jarDirs := pack.DefaultJarDirs
	isJarDir := func(d string) bool { return slices.Contains(jarDirs, d) }

	var jars []*jarFile
	var rootJars []*jarFile
	err = filepath.WalkDir(live, func(abs string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(live, abs)
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			switch {
			case IsRuntimeDir(rel):
				return filepath.SkipDir
			case IsWorldDir(abs):
				rep.Worlds = append(rep.Worlds, rel)
				addVolume(rel)
				return filepath.SkipDir
			case strings.EqualFold(d.Name(), "backups"):
				rep.Skipped = append(rep.Skipped, rel+"/ (backups; added as a volume)")
				addVolume(rel)
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || IsRuntimeFile(rel) {
			return nil
		}
		dir := path.Dir(rel)
		if strings.HasSuffix(strings.ToLower(rel), ".jar") {
			switch {
			case dir == ".":
				rootJars = append(rootJars, &jarFile{rel: rel, dir: dir, abs: abs})
			case isJarDir(dir):
				jars = append(jars, &jarFile{rel: rel, dir: dir, abs: abs})
			default:
				rep.Skipped = append(rep.Skipped, rel+" (nested jar, usually downloaded at runtime)")
			}
			return nil
		}
		if isDatabase(rel) {
			rep.Skipped = append(rep.Skipped, rel+" (database file: runtime state; mount it or switch the plugin to an external database)")
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > opts.MaxConfigSize {
			rep.Skipped = append(rep.Skipped, fmt.Sprintf("%s (%d MiB; too large to be config — consider a volume)", rel, info.Size()>>20))
			return nil
		}
		if err := copyFile(abs, filepath.Join(dst, filepath.FromSlash(rel))); err != nil {
			return err
		}
		rep.Configs++
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, j := range append(slices.Clone(jars), rootJars...) {
		if j.blob, err = opts.Cache.Add(j.abs); err != nil {
			return nil, err
		}
		j.info = readJarInfo(j.abs)
	}

	// The server jar: prefer server.jar, else the largest root jar.
	var serverJar *jarFile
	sort.Slice(rootJars, func(i, k int) bool { return rootJars[i].blob.Size > rootJars[k].blob.Size })
	for _, j := range rootJars {
		if j.rel == pack.ServerJar {
			serverJar = j
		}
	}
	if serverJar == nil && len(rootJars) > 0 {
		serverJar = rootJars[0]
	}
	for _, j := range rootJars {
		if j != serverJar {
			rep.Skipped = append(rep.Skipped, j.rel+" (extra jar in server root)")
		}
	}

	mc, loader := "", ""
	var serverDesc *descriptor.Descriptor
	if serverJar != nil {
		desc, summary, err := importServer(ctx, opts, dst, serverJar)
		if err != nil {
			return nil, err
		}
		rep.Server = summary
		mc, loader = source.ServerContext(desc)
		desc.Resolved = resolvedFor(desc, desc.Resolved)
		serverDesc = desc
	}

	matches := map[string]source.ModrinthMatch{}
	if !opts.Offline && len(jars) > 0 {
		var sha1s []string
		for _, j := range jars {
			sha1s = append(sha1s, j.blob.SHA1)
		}
		if matches, err = opts.Modrinth.LookupHashes(ctx, opts.HTTP, sha1s); err != nil {
			return nil, fmt.Errorf("identify jars on Modrinth: %w", err)
		}
	}

	used := map[string]bool{}
	for _, j := range jars {
		name := uniqueName(used, j.dir, descriptorName(j.abs, j.info))
		descPath := filepath.Join(dst, filepath.FromSlash(j.dir), name+".toml")
		if m, ok := matches[j.blob.SHA1]; ok {
			d := &descriptor.Descriptor{Source: "modrinth", Project: m.ProjectSlug, Version: m.Version.VersionNumber}
			r := &descriptor.Resolved{
				Version: m.Version.VersionNumber, URL: fileURL(m.Version, j.blob.SHA1),
				SHA256: j.blob.SHA256, Size: j.blob.Size, MCVersion: mc, Loader: loader, Compat: descriptor.CompatUnknown,
			}
			if mc != "" {
				r.Compat = descriptor.CompatNo
				if slices.Contains(m.Version.GameVersions, mc) {
					r.Compat = descriptor.CompatYes
				}
			}
			d.Resolved = resolvedFor(d, r)
			if err := descriptor.Write(descPath, d); err != nil {
				return nil, err
			}
			rep.Identified = append(rep.Identified, fmt.Sprintf("%s/%s.toml (modrinth %s %s)", j.dir, name, m.ProjectSlug, m.Version.VersionNumber))
			continue
		}
		if err := copyFile(j.abs, filepath.Join(dst, filepath.FromSlash(j.dir), name+".jar")); err != nil {
			return nil, err
		}
		if err := descriptor.Write(descPath, &descriptor.Descriptor{Source: "local"}); err != nil {
			return nil, err
		}
		rep.Vendored = append(rep.Vendored, fmt.Sprintf("%s/%s.toml (local, from %s)", j.dir, name, j.rel))
	}

	rep.Volumes = volumes
	if err := writeManifest(dst, opts, serverDesc, volumes); err != nil {
		return nil, err
	}
	return rep, nil
}

// importServer identifies the server jar (Paper/Folia via Fill checksums) or vendors it.
func importServer(ctx context.Context, opts Options, dst string, j *jarFile) (*descriptor.Descriptor, string, error) {
	mc := j.info.MCVersion
	if !opts.Offline && j.info.Paperclip && mc != "" {
		for _, project := range []string{"paper", "folia"} {
			build, ok := findFillBuild(ctx, opts, project, mc, j.blob.SHA256)
			if !ok {
				continue
			}
			d := &descriptor.Descriptor{Source: "papermc", Project: project, Version: mc, Build: strconv.Itoa(build.id)}
			d.Resolved = &descriptor.Resolved{
				Version: fmt.Sprintf("%s-%d", mc, build.id), URL: build.url, SHA256: j.blob.SHA256, Size: j.blob.Size,
				MCVersion: mc, Loader: project, Compat: descriptor.CompatYes,
			}
			return d, fmt.Sprintf("[server] papermc %s %s build %d", project, mc, build.id), nil
		}
	}
	if err := copyFile(j.abs, filepath.Join(dst, pack.ServerJar)); err != nil {
		return nil, "", err
	}
	d := &descriptor.Descriptor{Source: "local", MCVersion: mc}
	if j.info.Paperclip {
		d.Loader = "paper"
	}
	if j.rel != pack.ServerJar {
		d.Path = pack.ServerJar
	}
	return d, fmt.Sprintf("[server] local jar, from %s (switch to source = \"papermc\" etc. to manage it)", j.rel), nil
}

type fillBuild struct {
	id  int
	url string
}

func findFillBuild(ctx context.Context, opts Options, project, mc, sha256 string) (fillBuild, bool) {
	var builds []struct {
		ID        int `json:"id"`
		Downloads map[string]struct {
			Checksums struct {
				SHA256 string `json:"sha256"`
			} `json:"checksums"`
			URL string `json:"url"`
		} `json:"downloads"`
	}
	u := opts.PaperMC.BaseURL + "/projects/" + project + "/versions/" + mc + "/builds"
	if err := opts.HTTP.GetJSON(ctx, u, nil, &builds); err != nil {
		return fillBuild{}, false
	}
	for _, b := range builds {
		if dl, ok := b.Downloads["server:default"]; ok && strings.EqualFold(dl.Checksums.SHA256, sha256) {
			return fillBuild{id: b.ID, url: dl.URL}, true
		}
	}
	return fillBuild{}, false
}

// resolvedFor stamps the spec hash so the imported pin is considered fresh.
func resolvedFor(d *descriptor.Descriptor, r *descriptor.Resolved) *descriptor.Resolved {
	if r == nil {
		return nil
	}
	r.SpecHash = d.SpecHash()
	return r
}

func fileURL(v source.ModrinthVersion, sha1 string) string {
	for _, f := range v.Files {
		if strings.EqualFold(f.Hashes.SHA1, sha1) {
			return f.URL
		}
	}
	return ""
}

func uniqueName(used map[string]bool, dir, name string) string {
	candidate := name
	for i := 2; used[dir+"/"+candidate]; i++ {
		candidate = fmt.Sprintf("%s-%d", name, i)
	}
	used[dir+"/"+candidate] = true
	return candidate
}

func writeManifest(dst string, opts Options, server *descriptor.Descriptor, volumes []string) error {
	image := opts.Image
	if image == "" {
		image = "eclipse-temurin:25"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "name = %q\n", opts.Name)
	fmt.Fprintf(&b, "# extends = [\"common\"]\n\n")
	if server != nil {
		b.Write(descriptor.MarshalTable(pack.ServerTable, server))
		b.WriteString("\n")
	} else {
		b.WriteString("# No server jar was found; describe it here, e.g.\n# [server]\n# source = \"papermc\"\n# project = \"paper\"\n# version = \"1.21.10\"\n\n")
	}
	fmt.Fprintf(&b, "[runtime]\nimage = %q\n", image)
	if opts.MemoryMin != "" || opts.MemoryMax != "" {
		var parts []string
		if opts.MemoryMin != "" {
			parts = append(parts, fmt.Sprintf("min = %q", opts.MemoryMin))
		}
		if opts.MemoryMax != "" {
			parts = append(parts, fmt.Sprintf("max = %q", opts.MemoryMax))
		}
		fmt.Fprintf(&b, "memory = { %s }\n", strings.Join(parts, ", "))
	} else {
		fmt.Fprintf(&b, "# memory = { min = \"1G\", max = \"4G\" }\n")
	}
	fmt.Fprintf(&b, "jvm_flags = %s\n", tomlList(opts.JVMFlags))
	fmt.Fprintf(&b, "args = %s\n\n", tomlList(opts.Args))
	fmt.Fprintf(&b, "[build]\n# images = [\"registry.example.com/%s\"]\n# tags = [\"latest\", \"{git_sha}\"]\n\n", opts.Name)
	fmt.Fprintf(&b, "[volumes]\n# Runtime state: excluded from the image, mount these. Review this list!\npaths = %s\n\n", tomlList(volumes))
	fmt.Fprintf(&b, "[templates]\n# Files rendered with ${VAR} / ${VAR:-default} from the container environment at start.\ninclude = []\n")
	return os.WriteFile(filepath.Join(dst, pack.ManifestFile), []byte(b.String()), 0o644)
}

func tomlList(vs []string) string {
	if len(vs) == 0 {
		return "[]"
	}
	q := make([]string, len(vs))
	for i, v := range vs {
		q[i] = strconv.Quote(v)
	}
	if len(vs) <= 3 {
		return "[" + strings.Join(q, ", ") + "]"
	}
	return "[\n  " + strings.Join(q, ",\n  ") + ",\n]"
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
