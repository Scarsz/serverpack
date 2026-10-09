package resolve

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Scarsz/serverpack/internal/fetch"
	"github.com/Scarsz/serverpack/internal/httpx"
	"github.com/Scarsz/serverpack/internal/pack"
	"github.com/Scarsz/serverpack/internal/render"
	"github.com/Scarsz/serverpack/internal/srcbuild"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestBuildSource(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	fakeAPI(t)
	root := t.TempDir()

	// A Maven project in its own git repo, and a Gradle project inside the checkout (outside servers/ and bases/).
	remote := t.TempDir()
	write(t, remote, map[string]string{"pom.xml": "<project/>"})
	git(t, remote, "init", "--quiet")
	git(t, remote, "add", ".")
	git(t, remote, "commit", "--quiet", "-m", "init")
	head := git(t, remote, "rev-parse", "HEAD")

	write(t, root, map[string]string{
		"servers/a/server.toml":         "[server]\nsource = \"papermc\"\nversion = \"1.21.10\"\n",
		"servers/a/plugins/Fork.toml":   "source = \"build\"\ngit = \"" + filepath.ToSlash(remote) + "\"\nref = \"main\"\n",
		"servers/a/plugins/Local.toml":  "source = \"build\"\npath = \"../../../projects/Local\"\ntask = \"shadowJar\"\n",
		"projects/Local/build.gradle":   "",
		"projects/Local/src/Main.java":  "class Main {}",
		"servers/b/server.toml":         "[server]\nsource = \"papermc\"\nversion = \"1.21.10\"\n",
		"servers/b/plugins/Inside.toml": "source = \"build\"\npath = \"src\"\n",
		"servers/b/plugins/src/pom.xml": "<project/>",
	})
	cache := fetch.New(t.TempDir(), httpx.New())
	ctx := context.Background()

	p, _ := pack.Load(root, "a")
	changes, err := New(Options{HTTP: httpx.New(), Cache: cache}).Pack(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range changes {
		names = append(names, c.Entry.Name())
	}
	if strings.Join(names, ",") != "Fork,server" {
		t.Fatalf("changes = %v (the path project must not be locked)", names)
	}
	data, _ := os.ReadFile(filepath.Join(root, "servers/a/plugins/Fork.toml"))
	if !strings.Contains(string(data), "revision = \""+head+"\"") || !strings.Contains(string(data), "version = \"main@"+head[:12]+"\"") ||
		strings.Contains(string(data), "sha256") {
		t.Fatalf("git pin wrong:\n%s", data)
	}

	var built []string
	builder := &srcbuild.Builder{Cache: cache, Run: func(ctx context.Context, inv srcbuild.Invocation) error {
		built = append(built, filepath.Base(inv.Dir))
		out := "target/Fork-1.0.jar"
		if _, err := os.Stat(filepath.Join(inv.Dir, "build.gradle")); err == nil {
			out = "build/libs/Local-1.0.jar"
		}
		p := filepath.Join(inv.Dir, filepath.FromSlash(out))
		os.MkdirAll(filepath.Dir(p), 0o755)
		return os.WriteFile(p, fakeJar(filepath.Base(out)), 0o644)
	}}
	p, _ = pack.Load(root, "a")
	out := t.TempDir()
	if _, err := render.Render(ctx, p, render.Options{Cache: cache, Builder: builder, JarsDir: out, ConfigDir: out}); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"plugins/Fork.jar", "plugins/Local.jar"} {
		if err := fetch.CheckJar(filepath.Join(out, rel)); err != nil {
			t.Errorf("%s: %v", rel, err)
		}
	}
	if len(built) != 2 {
		t.Errorf("expected two builds, got %v", built)
	}
	if _, err := os.Stat(filepath.Join(out, "projects")); err == nil {
		t.Error("project sources leaked into the image")
	}

	// A project inside a server directory would ship its sources in the image.
	pb, _ := pack.Load(root, "b")
	if err := render.Validate(pb); err == nil || !strings.Contains(err.Error(), "inside server b") {
		t.Errorf("expected inside-layer error, got %v", err)
	}
}
