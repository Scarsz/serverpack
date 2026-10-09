package source

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Scarsz/serverpack/internal/descriptor"
	"github.com/Scarsz/serverpack/internal/httpx"
)

func cands() []candidate {
	// newest first
	return []candidate{
		{Version: "6.0-beta", Channel: descriptor.ChannelBeta, MCVersions: []string{"26.3"}, HasCompat: true, Loaders: []string{"paper"}},
		{Version: "5.2", MCVersions: []string{"26.1", "26.2"}, HasCompat: true, Loaders: []string{"paper"}},
		{Version: "5.1-fabric", MCVersions: []string{"1.21.10"}, HasCompat: true, Loaders: []string{"fabric"}},
		{Version: "5.1", Aliases: []string{"abc123"}, MCVersions: []string{"1.21.8", "1.21.10"}, HasCompat: true, Loaders: []string{"paper", "spigot"}},
		{Version: "5.0", MCVersions: []string{"1.21.1"}, HasCompat: true, Loaders: []string{"bukkit"}},
	}
}

func TestPickSelectors(t *testing.T) {
	ctx := &Context{MCVersion: "1.21.10", Loader: "paper"}
	cases := []struct {
		name       string
		d          descriptor.Descriptor
		ctx        *Context
		want       string
		wantCompat descriptor.Compat
		wantErr    string
	}{
		{name: "latest skips newer incompatible and wrong loader", d: descriptor.Descriptor{}, want: "5.1", wantCompat: "yes"},
		{name: "latest-any takes newest release", d: descriptor.Descriptor{Version: "latest-any"}, want: "5.2", wantCompat: "no"},
		{name: "beta channel allows beta", d: descriptor.Descriptor{Version: "latest-any", Channel: "beta"}, want: "6.0-beta"},
		{name: "exact pin regardless of compat", d: descriptor.Descriptor{Version: "5.0"}, want: "5.0", wantCompat: "no"},
		{name: "exact pin by alias", d: descriptor.Descriptor{Version: "abc123"}, want: "5.1", wantCompat: "yes"},
		{name: "mc_version override", d: descriptor.Descriptor{}, ctx: (&Context{MCVersion: "1.21.10", Loader: "paper"}).ForDescriptor(&descriptor.Descriptor{MCVersion: "26.2"}), want: "5.2", wantCompat: "yes"},
		{name: "no compatible version", d: descriptor.Descriptor{}, ctx: &Context{MCVersion: "1.19.4", Loader: "paper"}, wantErr: "no version supports Minecraft 1.19.4"},
		{name: "unknown mc behaves like latest-any", d: descriptor.Descriptor{}, ctx: &Context{Loader: "paper"}, want: "5.2", wantCompat: "unknown"},
		{name: "fabric loader filter", d: descriptor.Descriptor{}, ctx: &Context{MCVersion: "1.21.10", Loader: "fabric"}, want: "5.1-fabric"},
		{name: "missing exact", d: descriptor.Descriptor{Version: "9.9"}, wantErr: "not found"},
		{name: "bad channel", d: descriptor.Descriptor{Channel: "nightly"}, wantErr: "unknown channel"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.ctx
			if c == nil {
				c = ctx
			}
			got, compat, err := pick("test", cands(), &tc.d, c)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Version != tc.want {
				t.Errorf("picked %s, want %s", got.Version, tc.want)
			}
			if tc.wantCompat != "" && compat != tc.wantCompat {
				t.Errorf("compat = %s, want %s", compat, tc.wantCompat)
			}
		})
	}
}

func TestServerContext(t *testing.T) {
	mc, loader := ServerContext(&descriptor.Descriptor{Source: "papermc", Project: "paper", Version: "latest", Resolved: &descriptor.Resolved{MCVersion: "1.21.10"}})
	if mc != "1.21.10" || loader != "paper" {
		t.Errorf("got %s %s", mc, loader)
	}
	mc, loader = ServerContext(&descriptor.Descriptor{Source: "papermc", Project: "velocity", Version: "3.4.0-SNAPSHOT"})
	if mc != "" || loader != "velocity" {
		t.Errorf("proxy: got %q %q", mc, loader)
	}
	mc, loader = ServerContext(&descriptor.Descriptor{Source: "local", Loader: "paper", MCVersion: "1.21.4"})
	if mc != "1.21.4" || loader != "paper" {
		t.Errorf("local: got %s %s", mc, loader)
	}
}

// fixtureServer serves JSON/XML bodies by request path (+ raw query when present in the key).
func fixtureServer(t *testing.T, routes map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := routes[r.URL.Path+"?"+r.URL.RawQuery]
		if !ok {
			body, ok = routes[r.URL.Path]
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		if s, isStr := body.(string); isStr {
			w.Write([]byte(s))
			return
		}
		json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func resolve(t *testing.T, s Source, d descriptor.Descriptor, c Context) *Result {
	t.Helper()
	c.HTTP = httpx.New()
	c.HTTP.Retries = 0
	r, err := s.Resolve(context.Background(), &d, &c)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestPaperMC(t *testing.T) {
	srv := fixtureServer(t, map[string]any{
		"/v3/projects/paper/versions": map[string]any{"versions": []any{
			map[string]any{"version": map[string]any{"id": "26.3-rc-3"}, "builds": []int{1}},
			map[string]any{"version": map[string]any{"id": "1.21.10"}, "builds": []int{129, 130}},
		}},
		"/v3/projects/paper/versions/1.21.10/builds": []any{
			map[string]any{"id": 131, "channel": "BETA", "downloads": map[string]any{"server:default": map[string]any{"url": "https://x/131.jar"}}},
			map[string]any{"id": 130, "channel": "STABLE", "downloads": map[string]any{"server:default": map[string]any{
				"name": "paper-1.21.10-130.jar", "checksums": map[string]any{"sha256": "abc"}, "size": 5, "url": "https://x/130.jar"}}},
		},
	})
	p := &PaperMC{BaseURL: srv.URL + "/v3"}
	r := resolve(t, p, descriptor.Descriptor{Source: "papermc", Version: "latest"}, Context{})
	if r.Version != "1.21.10-130" || r.MCVersion != "1.21.10" || r.SHA256 != "abc" || r.Loader != "paper" {
		t.Errorf("got %+v", r)
	}
	r = resolve(t, p, descriptor.Descriptor{Source: "papermc", Version: "1.21.10", Channel: "beta"}, Context{})
	if r.Version != "1.21.10-131" {
		t.Errorf("beta build: got %+v", r)
	}
}

func TestModrinth(t *testing.T) {
	versions := []any{
		map[string]any{"id": "B", "version_number": "5.2", "version_type": "release", "game_versions": []string{"26.1"}, "loaders": []string{"paper"},
			"date_published": "2026-10-01T00:00:00Z", "files": []any{map[string]any{"url": "https://cdn/b.jar", "primary": true, "size": 2, "hashes": map[string]any{"sha1": "s1b"}}}},
		map[string]any{"id": "A", "version_number": "5.1", "version_type": "release", "game_versions": []string{"1.21.10"}, "loaders": []string{"paper"},
			"date_published": "2026-09-01T00:00:00Z", "files": []any{map[string]any{"url": "https://cdn/a.jar", "primary": true, "size": 1, "hashes": map[string]any{"sha1": "s1a", "sha512": "s5a"}}}},
	}
	srv := fixtureServer(t, map[string]any{"/v2/project/viaversion/version": versions})
	m := &Modrinth{BaseURL: srv.URL + "/v2"}
	// The fixture ignores the game_versions filter; pick still must choose by compatibility.
	r := resolve(t, m, descriptor.Descriptor{Source: "modrinth", Project: "viaversion"}, Context{MCVersion: "1.21.10", Loader: "paper"})
	if r.Version != "5.1" || r.URL != "https://cdn/a.jar" || r.SHA1 != "s1a" || r.Compat != descriptor.CompatYes {
		t.Errorf("got %+v", r)
	}
}

func TestHangar(t *testing.T) {
	v := func(name, channel string, mcs []string) map[string]any {
		return map[string]any{"name": name, "channel": map[string]any{"name": channel},
			"downloads": map[string]any{"PAPER": map[string]any{
				"fileInfo":    map[string]any{"name": name + ".jar", "sizeBytes": 3, "sha256Hash": "sha-" + name},
				"downloadUrl": "https://hangarcdn/" + name + ".jar"}},
			"platformDependencies": map[string]any{"PAPER": mcs}}
	}
	srv := fixtureServer(t, map[string]any{
		"/api/v1/projects/ViaVersion/versions": map[string]any{"pagination": map[string]any{"count": 3}, "result": []any{
			v("6.0", "Snapshot", []string{"1.21.10"}),
			v("5.9", "Release", []string{"26.1"}),
			v("5.8", "Release", []string{"1.21.8", "1.21.10"}),
		}},
		"/api/v1/projects/ViaVersion/versions/5.9": v("5.9", "Release", []string{"26.1"}),
	})
	h := &Hangar{BaseURL: srv.URL + "/api/v1"}
	r := resolve(t, h, descriptor.Descriptor{Source: "hangar", Project: "ViaVersion/ViaVersion"}, Context{MCVersion: "1.21.10", Loader: "paper"})
	if r.Version != "5.8" || r.SHA256 != "sha-5.8" || r.Compat != descriptor.CompatYes {
		t.Errorf("latest: got %+v", r)
	}
	r = resolve(t, h, descriptor.Descriptor{Source: "hangar", Project: "ViaVersion", Version: "5.9"}, Context{MCVersion: "1.21.10", Loader: "paper"})
	if r.Version != "5.9" || r.Compat != descriptor.CompatNo {
		t.Errorf("exact: got %+v", r)
	}
}

func TestGitHub(t *testing.T) {
	rel := map[string]any{"tag_name": "2.22.0", "assets": []any{
		map[string]any{"name": "EssentialsX-2.22.0.jar", "size": 4, "digest": "sha256:feed", "browser_download_url": "https://gh/ess.jar"},
		map[string]any{"name": "EssentialsXChat-2.22.0.jar", "browser_download_url": "https://gh/chat.jar"},
	}}
	srv := fixtureServer(t, map[string]any{
		"/repos/EssentialsX/Essentials/releases/latest":     rel,
		"/repos/EssentialsX/Essentials/releases/tags/v2.21": rel,
	})
	g := &GitHub{BaseURL: srv.URL}
	r := resolve(t, g, descriptor.Descriptor{Source: "github", Project: "EssentialsX/Essentials", Asset: "EssentialsX-*.jar"}, Context{})
	if r.Version != "2.22.0" || r.SHA256 != "feed" || r.URL != "https://gh/ess.jar" {
		t.Errorf("got %+v", r)
	}
	r = resolve(t, g, descriptor.Descriptor{Source: "github", Project: "EssentialsX/Essentials", Version: "2.21", Asset: "EssentialsX-*.jar"}, Context{})
	if r.Version != "2.22.0" {
		t.Errorf("tag v-prefix fallback failed: %+v", r)
	}
	c := Context{HTTP: httpx.New()}
	if _, err := g.Resolve(context.Background(), &descriptor.Descriptor{Project: "EssentialsX/Essentials"}, &c); err == nil || !strings.Contains(err.Error(), "several") {
		t.Errorf("expected ambiguous asset error, got %v", err)
	}
}

func TestJenkins(t *testing.T) {
	srv := fixtureServer(t, map[string]any{
		"/job/LuckPerms/lastSuccessfulBuild/api/json": map[string]any{"number": 1610, "artifacts": []any{
			map[string]any{"fileName": "LuckPerms-Bukkit-5.5.87.jar", "relativePath": "bukkit/loader/build/libs/LuckPerms-Bukkit-5.5.87.jar"},
			map[string]any{"fileName": "LuckPerms-Velocity-5.5.87.jar", "relativePath": "velocity/build/libs/LuckPerms-Velocity-5.5.87.jar"},
		}},
	})
	r := resolve(t, &Jenkins{}, descriptor.Descriptor{Source: "jenkins", URL: srv.URL + "/job/LuckPerms/", Asset: "LuckPerms-Bukkit-*.jar"}, Context{})
	if r.Version != "1610" || r.URL != srv.URL+"/job/LuckPerms/1610/artifact/bukkit/loader/build/libs/LuckPerms-Bukkit-5.5.87.jar" {
		t.Errorf("got %+v", r)
	}
}

func TestMaven(t *testing.T) {
	srv := fixtureServer(t, map[string]any{
		"/repo/com/example/thing/maven-metadata.xml": `<metadata><versioning><latest>2.0-SNAPSHOT</latest><release>1.5</release>
			<versions><version>1.5</version><version>2.0-SNAPSHOT</version></versions></versioning></metadata>`,
		"/repo/com/example/thing/2.0-SNAPSHOT/maven-metadata.xml": `<metadata><versioning><snapshot><timestamp>20261001.120000</timestamp><buildNumber>7</buildNumber></snapshot>
			<snapshotVersions><snapshotVersion><extension>jar</extension><value>2.0-20261001.120000-7</value></snapshotVersion></snapshotVersions></versioning></metadata>`,
	})
	m := &Maven{}
	r := resolve(t, m, descriptor.Descriptor{Source: "maven", Repo: srv.URL + "/repo/", Project: "com.example:thing"}, Context{})
	if r.Version != "1.5" || r.URL != srv.URL+"/repo/com/example/thing/1.5/thing-1.5.jar" {
		t.Errorf("release: got %+v", r)
	}
	r = resolve(t, m, descriptor.Descriptor{Source: "maven", Repo: srv.URL + "/repo", Project: "com.example:thing", Channel: "beta"}, Context{})
	if r.URL != srv.URL+"/repo/com/example/thing/2.0-SNAPSHOT/thing-2.0-20261001.120000-7.jar" {
		t.Errorf("snapshot: got %+v", r)
	}
}

func TestSpigot(t *testing.T) {
	srv := fixtureServer(t, map[string]any{
		"/v2/resources/1234":                 map[string]any{"name": "Thing", "testedVersions": []string{"1.20", "1.21"}},
		"/v2/resources/1234/versions/latest": map[string]any{"id": 99, "name": "3.1"},
		"/v2/resources/9089":                 map[string]any{"name": "EssentialsX", "external": true, "file": map[string]any{"externalUrl": "https://github.com/x"}},
	})
	s := &Spigot{BaseURL: srv.URL + "/v2"}
	r := resolve(t, s, descriptor.Descriptor{Source: "spigot", Project: "thing.1234"}, Context{MCVersion: "1.21.10"})
	if r.Version != "3.1" || r.URL != srv.URL+"/v2/resources/1234/download" || r.Compat != descriptor.CompatYes {
		t.Errorf("got %+v", r)
	}
	c := Context{HTTP: httpx.New()}
	if _, err := s.Resolve(context.Background(), &descriptor.Descriptor{Project: "1234", Version: "3.0"}, &c); err == nil || !strings.Contains(err.Error(), "only allows downloading the latest") {
		t.Errorf("expected latest-only error, got %v", err)
	}
	if _, err := s.Resolve(context.Background(), &descriptor.Descriptor{Project: "9089"}, &c); err == nil || !strings.Contains(err.Error(), "externally") {
		t.Errorf("expected external error, got %v", err)
	}
}

func TestCurseForge(t *testing.T) {
	srv := fixtureServer(t, map[string]any{
		"/v1/mods/search": map[string]any{"data": []any{map[string]any{"id": 42}}},
		"/v1/mods/42/files": map[string]any{"pagination": map[string]any{"totalCount": 2}, "data": []any{
			map[string]any{"id": 2, "displayName": "Mod 2.0", "fileName": "mod-2.0.jar", "releaseType": 1, "gameVersions": []string{"26.1", "Fabric"}, "downloadUrl": "https://edge/2.jar"},
			map[string]any{"id": 1, "displayName": "Mod 1.0", "fileName": "mod-1.0.jar", "releaseType": 1, "gameVersions": []string{"1.21.10", "Fabric"}, "downloadUrl": nil,
				"hashes": []any{map[string]any{"value": "aa", "algo": 1}}},
		}},
	})
	cf := &CurseForge{BaseURL: srv.URL + "/v1", Key: func() string { return "k" }}
	c := Context{MCVersion: "1.21.10", Loader: "fabric", HTTP: httpx.New()}
	_, err := cf.Resolve(context.Background(), &descriptor.Descriptor{Project: "some-mod"}, &c)
	if err == nil || !strings.Contains(err.Error(), "disallows third-party") {
		t.Fatalf("expected null downloadUrl error, got %v", err)
	}
	r := resolve(t, cf, descriptor.Descriptor{Project: "42", Version: "latest-any"}, Context{MCVersion: "1.21.10", Loader: "fabric"})
	if r.Version != "Mod 2.0" || r.URL != "https://edge/2.jar" {
		t.Errorf("got %+v", r)
	}
	noKey := &CurseForge{BaseURL: srv.URL + "/v1", Key: func() string { return "" }}
	if _, err := noKey.Resolve(context.Background(), &descriptor.Descriptor{Project: "42"}, &c); err == nil || !strings.Contains(err.Error(), "CURSEFORGE_API_KEY") {
		t.Errorf("expected missing key error, got %v", err)
	}
}
