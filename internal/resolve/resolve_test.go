package resolve

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Scarsz/serverpack/internal/fetch"
	"github.com/Scarsz/serverpack/internal/httpx"
	"github.com/Scarsz/serverpack/internal/pack"
	"github.com/Scarsz/serverpack/internal/render"
	"github.com/Scarsz/serverpack/internal/source"
)

func fakeJar(name string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("plugin.yml")
	w.Write([]byte("name: " + name + "\n"))
	zw.Close()
	return buf.Bytes()
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// fakeAPI serves a minimal Fill + Modrinth API and the jars they point at.
func fakeAPI(t *testing.T) *httptest.Server {
	t.Helper()
	jars := map[string][]byte{"/dl/paper.jar": fakeJar("paper"), "/dl/via.jar": fakeJar("ViaVersion"), "/dl/lp.jar": fakeJar("LuckPerms")}
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/fill/projects/paper/versions/1.21.10/builds", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]any{map[string]any{"id": 130, "channel": "STABLE", "downloads": map[string]any{"server:default": map[string]any{
			"url": srv.URL + "/dl/paper.jar", "checksums": map[string]any{"sha256": sum(jars["/dl/paper.jar"])}}}}})
	})
	modrinth := func(file string, mcs ...string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode([]any{map[string]any{"id": "x", "version_number": "1.0", "version_type": "release",
				"game_versions": mcs, "loaders": []string{"paper"}, "date_published": "2026-01-01T00:00:00Z",
				"files": []any{map[string]any{"url": srv.URL + file, "primary": true}}}})
		}
	}
	mux.HandleFunc("/modrinth/project/viaversion/version", modrinth("/dl/via.jar", "1.21.10"))
	mux.HandleFunc("/modrinth/project/luckperms/version", modrinth("/dl/lp.jar", "1.21.10"))
	for p, b := range jars {
		b := b
		mux.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) { w.Write(b) })
	}
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	source.Register("papermc", &source.PaperMC{BaseURL: srv.URL + "/fill"})
	source.Register("modrinth", &source.Modrinth{BaseURL: srv.URL + "/modrinth"})
	return srv
}

func write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLockAndRender(t *testing.T) {
	fakeAPI(t)
	root := t.TempDir()
	write(t, root, map[string]string{
		"bases/common/server.toml":              "[templates]\ninclude = [\"server.properties\"]\n",
		"bases/common/plugins/ViaVersion.toml":  "# shared\nsource = \"modrinth\"\nproject = \"viaversion\"\n",
		"bases/common/server.properties":        "rcon.password=${RCON}\n",
		"servers/a/server.toml":                 "extends = [\"common\"]\n\n[server]\nsource = \"papermc\" # comment\nversion = \"1.21.10\"\n\n[volumes]\npaths = [\"logs\", \"plugins/LuckPerms/data\"]\n",
		"servers/a/plugins/LuckPerms.toml":      "source = \"modrinth\"\nproject = \"luckperms\"\n",
		"servers/a/plugins/LuckPerms/data/x.db": "runtime state",
		"servers/a/plugins/Stray.jar":           "not referenced",
		"servers/a/plugins/Own.toml":            "source = \"local\"\n",
		"servers/a/plugins/Own.jar":             string(fakeJar("Own")),
		"servers/b/server.toml":                 "extends = [\"common\"]\n[server]\nsource = \"papermc\"\nversion = \"1.21.10\"\n",
	})
	cache := fetch.New(t.TempDir(), httpx.New())
	r := New(Options{HTTP: httpx.New(), Cache: cache})
	ctx := context.Background()

	p, err := pack.Load(root, "a")
	if err != nil {
		t.Fatal(err)
	}
	if err := render.Validate(p); err == nil {
		t.Fatal("unlocked pack should not validate")
	}
	changes, err := r.Pack(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 3 {
		t.Fatalf("expected 3 changes (server, ViaVersion, LuckPerms), got %d", len(changes))
	}
	via, _ := os.ReadFile(filepath.Join(root, "bases/common/plugins/ViaVersion.toml"))
	if !strings.HasPrefix(string(via), "# shared\n") || !strings.Contains(string(via), `mc_version = "1.21.10"`) {
		t.Errorf("shared descriptor not pinned in place:\n%s", via)
	}

	// Server b shares ViaVersion through the base: it is already fresh, so only b's server.toml is resolved.
	pb, _ := pack.Load(root, "b")
	changes, err = New(Options{HTTP: httpx.New(), Cache: cache}).Pack(ctx, pb)
	if err != nil || len(changes) != 1 || !changes[0].Entry.IsServer {
		t.Fatalf("b: %v %+v", err, changes)
	}

	// Lock again: nothing to do.
	p, _ = pack.Load(root, "a")
	if changes, err = New(Options{HTTP: httpx.New(), Cache: cache}).Pack(ctx, p); err != nil || len(changes) != 0 {
		t.Fatalf("relock: %v %+v", err, changes)
	}

	// Editing the spec makes it stale; Only restricts what gets touched.
	lpPath := filepath.Join(root, "servers/a/plugins/LuckPerms.toml")
	lp, _ := os.ReadFile(lpPath)
	os.WriteFile(lpPath, []byte(strings.Replace(string(lp), "project = \"luckperms\"\n", "project = \"luckperms\"\nchannel = \"beta\"\n", 1)), 0o644)
	p, _ = pack.Load(root, "a")
	if changes, _ = New(Options{HTTP: httpx.New(), Cache: cache, Only: []string{"ViaVersion"}}).Pack(ctx, p); len(changes) != 0 {
		t.Fatalf("Only should skip the stale LuckPerms descriptor: %+v", changes)
	}
	if err := render.Validate(p); err == nil || !strings.Contains(err.Error(), "changed since it was locked") {
		t.Fatalf("expected stale error, got %v", err)
	}
	if changes, err = New(Options{HTTP: httpx.New(), Cache: cache}).Pack(ctx, p); err != nil || len(changes) != 1 {
		t.Fatalf("stale relock: %v %+v", err, changes)
	}

	// Render.
	p, _ = pack.Load(root, "a")
	out := t.TempDir()
	var warnings []string
	rt, err := render.Render(ctx, p, render.Options{Cache: cache, JarsDir: out, ConfigDir: out, Warn: func(f string, a ...any) {
		warnings = append(warnings, f)
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"server.jar", "plugins/ViaVersion.jar", "plugins/LuckPerms.jar", "plugins/Own.jar", "server.properties", ".serverpack/templates/server.properties", ".serverpack/runtime.json"} {
		if _, err := os.Stat(filepath.Join(out, rel)); err != nil {
			t.Errorf("missing %s", rel)
		}
	}
	for _, rel := range []string{"plugins/Stray.jar", "plugins/LuckPerms.toml", "plugins/LuckPerms/data/x.db"} {
		if _, err := os.Stat(filepath.Join(out, rel)); err == nil {
			t.Errorf("%s should not be rendered", rel)
		}
	}
	if len(warnings) != 2 {
		t.Errorf("expected stray-jar and volume warnings, got %v", warnings)
	}
	if strings.Join(rt.Templates, ",") != "server.properties" || rt.MCVersion != "1.21.10" {
		t.Errorf("runtime = %+v", rt)
	}
	m, err := pack.LoadManifest(filepath.Join(root, "servers/a/server.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Server.Resolved == nil || m.Server.Resolved.Version != "1.21.10-130" || m.Server.Stale() {
		t.Errorf("server pin = %+v", m.Server.Resolved)
	}
	raw, _ := os.ReadFile(filepath.Join(root, "servers/a/server.toml"))
	if !strings.Contains(string(raw), "source = \"papermc\" # comment\nversion = \"1.21.10\"\n\n[server.resolved]\n") ||
		!strings.HasSuffix(string(raw), "\n\n[volumes]\npaths = [\"logs\", \"plugins/LuckPerms/data\"]\n") {
		t.Errorf("server.toml not edited in place:\n%s", raw)
	}
}
