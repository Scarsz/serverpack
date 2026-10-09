// Package srcbuild builds plugin jars from Gradle and Maven projects, from a local directory or a pinned
// git commit, and caches the results by source hash.
package srcbuild

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/Scarsz/serverpack/internal/descriptor"
	"github.com/Scarsz/serverpack/internal/fetch"
)

// Tool is a supported build tool.
type Tool string

const (
	Gradle Tool = "gradle"
	Maven  Tool = "maven"
)

type toolInfo struct {
	markers  []string // files that identify a project
	wrappers []string // wrapper scripts, in preference order (Windows script first)
	binary   string   // tool on PATH / in the image when there is no wrapper
	flags    []string // always passed
	task     string   // default task/goal
	output   string   // default output glob
	exclude  []string // output globs that are never the plugin jar
}

var tools = map[Tool]toolInfo{
	Gradle: {
		markers:  []string{"settings.gradle", "settings.gradle.kts", "build.gradle", "build.gradle.kts"},
		wrappers: []string{"gradlew.bat", "gradlew"},
		binary:   "gradle",
		flags:    []string{"--console=plain"},
		task:     "build",
		output:   "build/libs/*.jar",
		exclude:  []string{"*-sources.jar", "*-javadoc.jar", "*-plain.jar"},
	},
	Maven: {
		markers:  []string{"pom.xml"},
		wrappers: []string{"mvnw.cmd", "mvnw"},
		binary:   "mvn",
		flags:    []string{"-B", "--no-transfer-progress"},
		task:     "package",
		output:   "target/*.jar",
		exclude:  []string{"*-sources.jar", "*-javadoc.jar", "*-tests.jar", "original-*.jar"},
	},
}

// ValidateSpec checks the build-specific descriptor fields.
func ValidateSpec(d *descriptor.Descriptor) error {
	switch {
	case d.Path != "" && d.Git != "":
		return fmt.Errorf("set either path or git, not both")
	case d.Path == "" && d.Git == "":
		return fmt.Errorf("set path (a project directory) or git (a repository URL)")
	case d.Path != "" && d.Ref != "":
		return fmt.Errorf("ref only applies to git projects")
	}
	if d.Tool != "" {
		if _, ok := tools[Tool(d.Tool)]; !ok {
			return fmt.Errorf("unknown tool %q (want gradle or maven)", d.Tool)
		}
	}
	return nil
}

// DetectTool determines a project's build tool from its files, unless override is set.
func DetectTool(dir, override string) (Tool, error) {
	if override != "" {
		t := Tool(override)
		if _, ok := tools[t]; !ok {
			return "", fmt.Errorf("unknown tool %q (want gradle or maven)", override)
		}
		return t, nil
	}
	var found []Tool
	for _, t := range []Tool{Gradle, Maven} {
		for _, m := range tools[t].markers {
			if _, err := os.Stat(filepath.Join(dir, m)); err == nil {
				found = append(found, t)
				break
			}
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return "", fmt.Errorf("%s is not a Gradle or Maven project (no settings.gradle(.kts), build.gradle(.kts) or pom.xml)", dir)
	}
	return "", fmt.Errorf("%s has both Gradle and Maven build files; set tool = \"gradle\" or \"maven\"", dir)
}

// skipDirs are never part of a project's source hash.
var skipDirs = map[string]bool{
	"build": true, "target": true, ".gradle": true, ".git": true, ".idea": true,
	"out": true, "run": true, ".kotlin": true, "node_modules": true,
}

// SourceHash hashes a project's files (path and content), ignoring build outputs and tool caches.
func SourceHash(dir string) (string, error) {
	h := sha256.New()
	var files []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel != "." && (skipDirs[d.Name()] || rel == ".mvn/wrapper/dists") {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	for _, rel := range files {
		f, err := os.Open(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00", rel)
		_, err = io.Copy(h, f)
		f.Close()
		if err != nil {
			return "", err
		}
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Spec describes one build.
type Spec struct {
	Name     string // for messages
	Dir      string // local project directory (path projects)
	Git      string // repository URL (git projects)
	Revision string // pinned commit (git projects)
	Tool     string
	Task     string
	Output   string
	Docker   string // image to build in; empty builds on the host
}

// SpecFor converts a descriptor into a Spec. dir is the resolved project directory for path projects.
func SpecFor(name string, d *descriptor.Descriptor, dir string) Spec {
	s := Spec{Name: name, Tool: d.Tool, Task: d.Task, Output: d.Output, Docker: d.Docker}
	if d.Git != "" {
		s.Git = d.Git
		if d.Resolved != nil {
			s.Revision = d.Resolved.Revision
		}
	} else {
		s.Dir = dir
	}
	return s
}

// Invocation is one build command.
type Invocation struct {
	Dir  string
	Argv []string
	Env  []string // extra environment
}

// Builder runs builds and caches their jars.
type Builder struct {
	Cache  *fetch.Cache
	Docker string    // docker CLI for Spec.Docker builds (default "docker")
	Stderr io.Writer // build output
	// Run executes a build; nil runs the command. Tests replace it.
	Run func(ctx context.Context, inv Invocation) error
}

func (b *Builder) stderr() io.Writer {
	if b.Stderr == nil {
		return io.Discard
	}
	return b.Stderr
}

// Jar returns the path of the built jar for spec, building it unless an identical build is cached.
func (b *Builder) Jar(ctx context.Context, spec Spec) (string, error) {
	var srcHash string
	if spec.Git != "" {
		if spec.Revision == "" {
			return "", fmt.Errorf("%s: no pinned revision; run `serverpack lock`", spec.Name)
		}
		srcHash = "git:" + spec.Revision
	} else {
		h, err := SourceHash(spec.Dir)
		if err != nil {
			return "", fmt.Errorf("%s: %w", spec.Name, err)
		}
		srcHash = "dir:" + h
	}
	key := cacheKey(srcHash, spec)
	if p, ok := b.lookup(key); ok {
		return p, nil
	}

	dir := spec.Dir
	if spec.Git != "" {
		wt, cleanup, err := b.checkout(ctx, spec.Git, spec.Revision)
		if err != nil {
			return "", fmt.Errorf("%s: %w", spec.Name, err)
		}
		defer cleanup()
		dir = wt
	}
	tool, err := DetectTool(dir, spec.Tool)
	if err != nil {
		return "", fmt.Errorf("%s: %w", spec.Name, err)
	}
	inv, err := b.invocation(tool, dir, spec)
	if err != nil {
		return "", fmt.Errorf("%s: %w", spec.Name, err)
	}

	fmt.Fprintf(b.stderr(), "building %s with %s: %s\n", spec.Name, tool, strings.Join(inv.Argv, " "))
	start := time.Now().Add(-2 * time.Second) // tolerate coarse filesystem timestamps
	run := b.Run
	if run == nil {
		run = b.exec
	}
	if err := run(ctx, inv); err != nil {
		return "", fmt.Errorf("%s: %s build failed: %w", spec.Name, tool, err)
	}

	out, err := pickOutput(dir, tool, spec.Output, start)
	if err != nil {
		return "", fmt.Errorf("%s: %w", spec.Name, err)
	}
	if err := fetch.CheckJar(out); err != nil {
		return "", fmt.Errorf("%s: %s %w", spec.Name, out, err)
	}
	blob, err := b.Cache.Add(out)
	if err != nil {
		return "", err
	}
	if err := b.store(key, blob.SHA256); err != nil {
		return "", err
	}
	return blob.Path, nil
}

func cacheKey(srcHash string, spec Spec) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{"v1", srcHash, spec.Tool, spec.Task, spec.Output, spec.Docker}, "\x00")))
	return hex.EncodeToString(sum[:])
}

func (b *Builder) indexPath(key string) string { return filepath.Join(b.Cache.Dir, "build", key) }

func (b *Builder) lookup(key string) (string, bool) {
	data, err := os.ReadFile(b.indexPath(key))
	if err != nil {
		return "", false
	}
	return b.Cache.Lookup(strings.TrimSpace(string(data)))
}

func (b *Builder) store(key, sha string) error {
	p := b.indexPath(key)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(sha+"\n"), 0o644)
}

func (b *Builder) invocation(tool Tool, dir string, spec Spec) (Invocation, error) {
	info := tools[tool]
	task := spec.Task
	if task == "" {
		task = info.task
	}
	args := append(append([]string{}, info.flags...), strings.Fields(task)...)

	if spec.Docker != "" {
		docker := b.Docker
		if docker == "" {
			docker = "docker"
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			return Invocation{}, err
		}
		argv := []string{docker, "run", "--rm", "-v", abs + ":/project", "-w", "/project"}
		switch tool {
		case Gradle:
			argv = append(argv, "-v", b.toolCache("gradle-home")+":/gradle-home", "-e", "GRADLE_USER_HOME=/gradle-home")
		case Maven:
			argv = append(argv, "-v", b.toolCache("m2")+":/m2")
			args = append(args, "-Dmaven.repo.local=/m2/repository")
		}
		if runtime.GOOS != "windows" {
			argv = append(argv, "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()))
		}
		argv = append(argv, spec.Docker)
		// Inside the (Linux) container, prefer the project's unix wrapper.
		unixWrapper := info.wrappers[len(info.wrappers)-1]
		if _, err := os.Stat(filepath.Join(dir, unixWrapper)); err == nil {
			argv = append(argv, "sh", "./"+unixWrapper)
		} else {
			argv = append(argv, info.binary)
		}
		return Invocation{Dir: dir, Argv: append(argv, args...)}, nil
	}

	for _, w := range info.wrappers {
		isWindowsScript := strings.HasSuffix(w, ".bat") || strings.HasSuffix(w, ".cmd")
		if isWindowsScript != (runtime.GOOS == "windows") {
			continue
		}
		p := filepath.Join(dir, w)
		if _, err := os.Stat(p); err != nil {
			continue
		}
		if isWindowsScript {
			return Invocation{Dir: dir, Argv: append([]string{p}, args...)}, nil
		}
		return Invocation{Dir: dir, Argv: append([]string{"sh", p}, args...)}, nil
	}
	return Invocation{Dir: dir, Argv: append([]string{info.binary}, args...)}, nil
}

func (b *Builder) toolCache(name string) string {
	p := filepath.Join(b.Cache.Dir, name)
	os.MkdirAll(p, 0o755)
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

func (b *Builder) exec(ctx context.Context, inv Invocation) error {
	if _, err := exec.LookPath(inv.Argv[0]); err != nil {
		for tool, info := range tools {
			if inv.Argv[0] == info.binary {
				return fmt.Errorf("the project has no %s wrapper and %s is not on PATH; add a wrapper, install %s, or set docker = \"<image>\" to build in a container",
					tool, info.binary, info.binary)
			}
		}
		return err
	}
	cmd := exec.CommandContext(ctx, inv.Argv[0], inv.Argv[1:]...)
	cmd.Dir = inv.Dir
	cmd.Env = append(os.Environ(), inv.Env...)
	cmd.Stdout, cmd.Stderr = b.stderr(), b.stderr()
	return cmd.Run()
}

// pickOutput selects the built jar: the single match modified during this build, or, when the build
// was up to date, the newest match.
func pickOutput(dir string, tool Tool, glob string, since time.Time) (string, error) {
	info := tools[tool]
	if glob == "" {
		glob = info.output
	}
	matches, err := doublestar.Glob(os.DirFS(dir), filepath.ToSlash(glob))
	if err != nil {
		return "", fmt.Errorf("output %q: %w", glob, err)
	}
	type cand struct {
		rel string
		mod time.Time
	}
	var cands []cand
	for _, rel := range matches {
		if excluded(path.Base(rel), info.exclude) {
			continue
		}
		st, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		cands = append(cands, cand{rel, st.ModTime()})
	}
	if len(cands) == 0 {
		return "", fmt.Errorf("the build produced no jar matching output %q", glob)
	}
	var fresh []string
	for _, c := range cands {
		if !c.mod.Before(since) {
			fresh = append(fresh, c.rel)
		}
	}
	switch {
	case len(fresh) == 1:
		return filepath.Join(dir, filepath.FromSlash(fresh[0])), nil
	case len(fresh) > 1:
		sort.Strings(fresh)
		return "", fmt.Errorf("output %q matches several jars from this build (%s); set output to a narrower glob", glob, strings.Join(fresh, ", "))
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].mod.After(cands[j].mod) })
	return filepath.Join(dir, filepath.FromSlash(cands[0].rel)), nil
}

func excluded(name string, patterns []string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(p, name); ok {
			return true
		}
	}
	return false
}

var errNoGit = errors.New("git is not installed or not on PATH")
