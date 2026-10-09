package pack

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func testRepo(t *testing.T) string {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"bases/java/server.toml": `
[server]
source = "papermc"
version = "1.21.4"

[runtime]
image = "eclipse-temurin:21"
jvm_flags = ["-Dbase=1"]
[build]
images = ["registry.example/base"]
tags = ["latest"]
`,
		"bases/common/server.toml": `
extends = ["java"]
[runtime]
image = "eclipse-temurin:25"
memory = { min = "1G", max = "2G" }
[volumes]
paths = ["logs"]
[templates]
include = ["server.properties"]
`,
		"bases/common/plugins/LuckPerms.toml":       "source = \"modrinth\"\nproject = \"luckperms\"\n",
		"bases/common/plugins/ViaVersion.toml":      "source = \"modrinth\"\nproject = \"viaversion\"\n",
		"bases/common/plugins/LuckPerms/config.yml": "base: true\n",
		"bases/common/server.properties":            "motd=base\n",
		"servers/plot251/server.toml": `
extends = ["common"]
[server]
source = "papermc"
project = "paper"
version = "1.21.10"
[runtime]
memory = { max = "24576M" }
jvm_flags_append = ["-Dserver=1"]
args = []
[build]
images_append = ["ghcr.io/x/plot251"]
[volumes]
paths = ["plugins/PlotSquared/backups/", "logs"]
`,
		"servers/inherits/server.toml":                         `extends = ["common"]`,
		"servers/plot251/server.properties":                    "motd=plot251\n",
		"servers/plot251/plugins/ViaVersion.toml":              "disabled = true\n",
		"servers/plot251/plugins/Internal.toml":                "source = \"local\"\n",
		"servers/plot251/plugins/Internal.jar":                 "jar",
		"servers/plot251/plugins/PlotSquared/config/keep.toml": "not = \"a descriptor\"\n",
		"servers/plot251/.gitignore":                           "x\n",
	})
	return root
}

func TestLoadLayering(t *testing.T) {
	root := testRepo(t)
	if got, err := FindRoot(filepath.Join(root, "servers", "plot251", "plugins")); err != nil || got != root {
		t.Fatalf("FindRoot = %q, %v", got, err)
	}
	p, err := Load(root, "plot251")
	if err != nil {
		t.Fatal(err)
	}
	if names := layerNames(p); !slices.Equal(names, []string{"java", "common", "plot251"}) {
		t.Fatalf("layer order = %v", names)
	}
	m := p.Manifest
	if m.Name != "plot251" || m.Runtime.Image != "eclipse-temurin:25" || m.Runtime.Memory.Min != "1G" || m.Runtime.Memory.Max != "24576M" {
		t.Errorf("scalar merge wrong: %+v", m.Runtime)
	}
	if !slices.Equal(m.Runtime.JVMFlags, []string{"-Dbase=1", "-Dserver=1"}) {
		t.Errorf("jvm flags = %v", m.Runtime.JVMFlags)
	}
	if m.Runtime.Args == nil || len(m.Runtime.Args) != 0 {
		t.Errorf("explicit empty args should be set and empty: %#v", m.Runtime.Args)
	}
	if !slices.Equal(m.Build.Images, []string{"registry.example/base", "ghcr.io/x/plot251"}) || !slices.Equal(m.Build.Tags, []string{"latest"}) {
		t.Errorf("build merge = %+v", m.Build)
	}
	if !slices.Equal(m.Volumes.Paths, []string{"logs", "plugins/PlotSquared/backups"}) {
		t.Errorf("volumes = %v", m.Volumes.Paths)
	}

	if f := p.Files["server.properties"]; f == nil || f.Layer.Name != "plot251" {
		t.Errorf("server.properties should come from server layer")
	}
	if f := p.Files["plugins/LuckPerms/config.yml"]; f == nil || f.Layer.Name != "common" {
		t.Errorf("inherited config missing")
	}
	if _, ok := p.Files[".gitignore"]; ok {
		t.Errorf(".gitignore should be ignored")
	}
	if _, ok := p.Files[ManifestFile]; ok {
		t.Errorf("manifest should not be in tree")
	}

	var names []string
	for _, e := range p.Entries {
		names = append(names, e.Name()+"="+e.Target)
	}
	want := []string{"server=server.jar", "Internal=plugins/Internal.jar", "LuckPerms=plugins/LuckPerms.jar"}
	if !slices.Equal(names, want) {
		t.Errorf("entries = %v, want %v", names, want)
	}
	srv := p.Server()
	if srv.Desc.Version != "1.21.10" || srv.File.Layer.Name != "plot251" || srv.Table() != "server.resolved" ||
		srv.File.Abs != filepath.Join(root, "servers", "plot251", "server.toml") {
		t.Errorf("server entry = %+v (layer %s)", srv.Desc, srv.File.Layer.Name)
	}

	// A server without its own [server] table inherits the whole table (and its pin location) from a base.
	inh, err := Load(root, "inherits")
	if err != nil {
		t.Fatal(err)
	}
	if s := inh.Server(); s == nil || s.Desc.Version != "1.21.4" || s.File.Layer.Name != "java" {
		t.Errorf("inherited server = %+v", s)
	}
	if len(p.Disabled) != 1 || p.Disabled[0].Rel != "plugins/ViaVersion.toml" {
		t.Errorf("disabled = %v", p.Disabled)
	}
	if !p.UnderVolume("plugins/PlotSquared/backups/a.zip") || p.UnderVolume("plugins/PlotSquared/backupsx") {
		t.Errorf("UnderVolume wrong")
	}
	if e := p.FindEntry("Internal"); e == nil || e.LocalPath() != filepath.Join(root, "servers", "plot251", "plugins", "Internal.jar") {
		t.Errorf("local path wrong")
	}
}

func TestExtendsCycle(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"bases/a/server.toml":   `extends = ["b"]`,
		"bases/b/server.toml":   `extends = ["a"]`,
		"servers/s/server.toml": `extends = ["a"]`,
	})
	if _, err := Load(root, "s"); err == nil {
		t.Fatal("expected cycle error")
	}
}

func layerNames(p *Pack) []string {
	var out []string
	for _, l := range p.Layers {
		out = append(out, l.Name)
	}
	return out
}
