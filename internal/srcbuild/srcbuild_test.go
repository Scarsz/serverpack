package srcbuild

import (
	"archive/zip"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Scarsz/serverpack/internal/descriptor"
	"github.com/Scarsz/serverpack/internal/fetch"
	"github.com/Scarsz/serverpack/internal/httpx"
)

func write(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func writeJar(t *testing.T, path, marker string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, _ := zw.Create("plugin.yml")
	w.Write([]byte("name: " + marker + "\n"))
	zw.Close()
	f.Close()
}

func TestDetectTool(t *testing.T) {
	g, m, both, none := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	write(t, g, map[string]string{"settings.gradle.kts": ""})
	write(t, m, map[string]string{"pom.xml": ""})
	write(t, both, map[string]string{"build.gradle": "", "pom.xml": ""})
	if tool, err := DetectTool(g, ""); err != nil || tool != Gradle {
		t.Errorf("gradle: %v %v", tool, err)
	}
	if tool, err := DetectTool(m, ""); err != nil || tool != Maven {
		t.Errorf("maven: %v %v", tool, err)
	}
	if _, err := DetectTool(both, ""); err == nil || !strings.Contains(err.Error(), "both") {
		t.Errorf("both: %v", err)
	}
	if tool, err := DetectTool(both, "maven"); err != nil || tool != Maven {
		t.Errorf("override: %v %v", tool, err)
	}
	if _, err := DetectTool(none, ""); err == nil {
		t.Error("expected error for non-project")
	}
}

func TestValidateSpec(t *testing.T) {
	cases := map[string]descriptor.Descriptor{
		"either":   {Source: "build"},
		"both":     {Source: "build", Path: "x", Git: "y"},
		"ref-path": {Source: "build", Path: "x", Ref: "main"},
		"tool":     {Source: "build", Path: "x", Tool: "ant"},
	}
	for name, d := range cases {
		if err := ValidateSpec(&d); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	if err := ValidateSpec(&descriptor.Descriptor{Git: "https://x/y.git", Ref: "main", Tool: "gradle"}); err != nil {
		t.Error(err)
	}
}

func TestSourceHashIgnoresOutputs(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{"build.gradle.kts": "a", "src/Main.java": "class Main {}"})
	h1, _ := SourceHash(dir)
	write(t, dir, map[string]string{"build/libs/x.jar": "out", ".gradle/cache": "c", "target/x.jar": "t", "sub/build/y": "y"})
	h2, _ := SourceHash(dir)
	if h1 != h2 {
		t.Error("build outputs changed the source hash")
	}
	write(t, dir, map[string]string{"src/Main.java": "class Main { int x; }"})
	if h3, _ := SourceHash(dir); h3 == h1 {
		t.Error("source edit did not change the hash")
	}
}

func TestPickOutput(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-time.Hour)
	stale := filepath.Join(dir, "build/libs/P-1.0.jar")
	writeJar(t, stale, "old")
	os.Chtimes(stale, old, old)
	since := time.Now().Add(-time.Minute)

	// Up to date: nothing fresh, the newest match wins.
	writeJar(t, filepath.Join(dir, "build/libs/P-1.1.jar"), "new")
	os.Chtimes(filepath.Join(dir, "build/libs/P-1.1.jar"), old.Add(time.Minute), old.Add(time.Minute))
	if got, err := pickOutput(dir, Gradle, "", since); err != nil || filepath.Base(got) != "P-1.1.jar" {
		t.Errorf("stale-newest: %s %v", got, err)
	}

	// One fresh jar (plus excluded sources/plain jars) beats stale ones.
	writeJar(t, filepath.Join(dir, "build/libs/P-1.2.jar"), "fresh")
	writeJar(t, filepath.Join(dir, "build/libs/P-1.2-sources.jar"), "src")
	writeJar(t, filepath.Join(dir, "build/libs/P-1.2-plain.jar"), "plain")
	if got, err := pickOutput(dir, Gradle, "", since); err != nil || filepath.Base(got) != "P-1.2.jar" {
		t.Errorf("fresh: %s %v", got, err)
	}

	// Two fresh candidates are ambiguous until output narrows them.
	writeJar(t, filepath.Join(dir, "build/libs/P-1.2-all.jar"), "shadow")
	if _, err := pickOutput(dir, Gradle, "", since); err == nil || !strings.Contains(err.Error(), "several") {
		t.Errorf("expected ambiguity, got %v", err)
	}
	if got, err := pickOutput(dir, Gradle, "build/libs/*-all.jar", since); err != nil || filepath.Base(got) != "P-1.2-all.jar" {
		t.Errorf("narrowed: %s %v", got, err)
	}

	// Maven: maven-shade's original-*.jar is never the plugin.
	m := t.TempDir()
	writeJar(t, filepath.Join(m, "target/P-1.0.jar"), "shaded")
	writeJar(t, filepath.Join(m, "target/original-P-1.0.jar"), "original")
	if got, err := pickOutput(m, Maven, "", since); err != nil || filepath.Base(got) != "P-1.0.jar" {
		t.Errorf("maven: %s %v", got, err)
	}
	if _, err := pickOutput(t.TempDir(), Maven, "", since); err == nil {
		t.Error("expected no-output error")
	}
}

// fakeBuilder returns a Builder whose builds write <project>/<out> and record each invocation.
func fakeBuilder(t *testing.T, out string, calls *[]Invocation) *Builder {
	cache := fetch.New(t.TempDir(), httpx.New())
	return &Builder{Cache: cache, Run: func(ctx context.Context, inv Invocation) error {
		*calls = append(*calls, inv)
		writeJar(t, filepath.Join(inv.Dir, filepath.FromSlash(out)), "built")
		return nil
	}}
}

func TestJarCachesPathBuilds(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{"pom.xml": "<project/>", "src/A.java": "a"})
	var calls []Invocation
	b := fakeBuilder(t, "target/P-1.0.jar", &calls)
	spec := Spec{Name: "P", Dir: dir}
	ctx := context.Background()

	first, err := b.Jar(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || !slices.Contains(calls[0].Argv, "package") || !slices.Contains(calls[0].Argv, "-B") {
		t.Fatalf("maven defaults not applied: %+v", calls)
	}
	if _, err := b.Jar(ctx, spec); err != nil || len(calls) != 1 {
		t.Fatalf("second build should be a cache hit (%d builds, %v)", len(calls), err)
	}
	write(t, dir, map[string]string{"src/A.java": "changed"})
	second, err := b.Jar(ctx, spec)
	if err != nil || len(calls) != 2 {
		t.Fatalf("source change should rebuild (%d builds, %v)", len(calls), err)
	}
	if first == "" || second == "" {
		t.Fatal("no jar path returned")
	}
	if _, err := b.Jar(ctx, Spec{Name: "P", Dir: dir, Task: "package -DskipTests"}); err != nil || len(calls) != 3 {
		t.Fatalf("task change should rebuild (%d builds, %v)", len(calls), err)
	}
	if last := calls[2].Argv; !slices.Contains(last, "-DskipTests") {
		t.Errorf("task args not passed: %v", last)
	}
}

func TestDockerInvocation(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{"build.gradle.kts": "", "gradlew": "#!/bin/sh"})
	b := &Builder{Cache: fetch.New(t.TempDir(), httpx.New()), Docker: "docker"}
	inv, err := b.invocation(Gradle, dir, Spec{Docker: "eclipse-temurin:21", Task: "shadowJar"})
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Join(inv.Argv, " ")
	for _, want := range []string{"docker run --rm -v ", ":/project -w /project", "GRADLE_USER_HOME=/gradle-home", "eclipse-temurin:21 sh ./gradlew --console=plain shadowJar"} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv missing %q: %s", want, argv)
		}
	}
	m := t.TempDir()
	write(t, m, map[string]string{"pom.xml": ""})
	inv, _ = b.invocation(Maven, m, Spec{Docker: "maven:3-eclipse-temurin-21"})
	if argv := strings.Join(inv.Argv, " "); !strings.Contains(argv, "maven:3-eclipse-temurin-21 mvn -B --no-transfer-progress package -Dmaven.repo.local=/m2/repository") {
		t.Errorf("maven docker argv: %s", argv)
	}
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestGitBuild(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := t.TempDir()
	gitRun(t, repo, "init", "--quiet")
	write(t, repo, map[string]string{"pom.xml": "<project/>", "src/A.java": "v1"})
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "--quiet", "-m", "v1")
	gitRun(t, repo, "tag", "-a", "v1.0", "-m", "release")
	v1 := gitRun(t, repo, "rev-parse", "HEAD")
	url := filepath.ToSlash(repo)
	ctx := context.Background()

	for _, ref := range []string{"", "main", "v1.0", v1} {
		if got, err := LsRemote(ctx, url, ref); err != nil || got != v1 {
			t.Errorf("LsRemote(%q) = %s, %v; want %s", ref, got, err, v1)
		}
	}
	if _, err := LsRemote(ctx, url, "nope"); err == nil {
		t.Error("expected missing ref error")
	}
	if _, err := LsRemote(ctx, url, v1[:8]); err == nil || !strings.Contains(err.Error(), "abbreviated") {
		t.Errorf("expected abbreviated-hash error, got %v", err)
	}

	write(t, repo, map[string]string{"src/A.java": "v2"})
	gitRun(t, repo, "commit", "--quiet", "-am", "v2")
	v2, _ := LsRemote(ctx, url, "main")
	if v2 == v1 {
		t.Fatal("main did not move")
	}

	var calls []Invocation
	b := fakeBuilder(t, "target/P.jar", &calls)
	var seen string
	inner := b.Run
	b.Run = func(ctx context.Context, inv Invocation) error {
		data, _ := os.ReadFile(filepath.Join(inv.Dir, "src", "A.java"))
		seen = string(data)
		return inner(ctx, inv)
	}
	if _, err := b.Jar(ctx, Spec{Name: "P", Git: url, Revision: v1}); err != nil {
		t.Fatal(err)
	}
	if seen != "v1" {
		t.Errorf("built %q, want the pinned v1 sources", seen)
	}
	if _, err := b.Jar(ctx, Spec{Name: "P", Git: url, Revision: v1}); err != nil || len(calls) != 1 {
		t.Fatalf("pinned rebuild should hit the cache (%d builds, %v)", len(calls), err)
	}
	if _, err := b.Jar(ctx, Spec{Name: "P", Git: url, Revision: v2}); err != nil || seen != "v2" || len(calls) != 2 {
		t.Fatalf("new revision: seen %q, %d builds, %v", seen, len(calls), err)
	}
	if entries, _ := os.ReadDir(filepath.Join(b.Cache.Dir, "build-work")); len(entries) != 0 {
		t.Errorf("worktrees not cleaned up: %v", entries)
	}
	if _, err := b.Jar(ctx, Spec{Name: "P", Git: url}); err == nil {
		t.Error("expected missing-revision error")
	}
}
