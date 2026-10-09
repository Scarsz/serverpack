// Package check validates packs without touching the network.
package check

import (
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/Scarsz/serverpack/internal/pack"
	"github.com/Scarsz/serverpack/internal/render"
	"github.com/Scarsz/serverpack/internal/source"
)

// Level is a finding's severity.
type Level string

const (
	Error   Level = "error"
	Warning Level = "warning"
)

// Finding is one problem found in a pack.
type Finding struct {
	Level Level
	Msg   string
}

// Pack checks a loaded pack.
func Pack(p *pack.Pack) []Finding {
	var out []Finding
	add := func(l Level, format string, args ...any) {
		out = append(out, Finding{Level: l, Msg: fmt.Sprintf(format, args...)})
	}

	for _, err := range render.Problems(p) {
		add(Error, "%s", err)
	}

	for _, v := range p.Manifest.Volumes.Paths {
		if v == "." || v == "" || strings.HasPrefix(v, "..") || path.IsAbs(v) {
			add(Error, "%s: volume path %q must be a relative path inside /data", p.Name, v)
		}
	}

	mc, loader := "", ""
	if srv := p.Server(); srv != nil {
		mc, loader = source.ServerContext(srv.Desc)
	}
	for _, e := range p.Entries {
		d := e.Desc
		if _, err := d.ChannelRank(); err != nil {
			add(Error, "%s: %s: %s", p.Name, e.Where(), err)
		}
		if e.IsServer || d.Source == "local" || d.Resolved == nil {
			continue
		}
		want := mc
		if d.MCVersion != "" {
			want = d.MCVersion
		}
		r := d.Resolved
		switch {
		case want != "" && r.MCVersion != want:
			add(Warning, "%s: %s was resolved for Minecraft %s but the server runs %s; run `serverpack update %s %s`",
				p.Name, e.Where(), orUnknown(r.MCVersion), want, p.Self.Name, e.Name())
		case r.Compat == "no":
			add(Warning, "%s: %s %s does not declare support for Minecraft %s", p.Name, e.Where(), r.Version, r.MCVersion)
		}
		if loader != "" && d.Loader == "" && r.Loader != "" && r.Loader != loader {
			add(Warning, "%s: %s was resolved for %s but the server is %s; run `serverpack update %s %s`", p.Name, e.Where(), r.Loader, loader, p.Self.Name, e.Name())
		}
	}

	render.ConfigFiles(p, func(format string, args ...any) { add(Warning, format, args...) })
	for _, pat := range p.Manifest.Templates.Include {
		matched := false
		for rel := range p.Files {
			if render.MatchGlob(pat, rel) {
				matched = true
				break
			}
		}
		if !matched {
			add(Warning, "%s: template pattern %q matches no files", p.Name, pat)
		}
	}

	if p.Self.Kind == pack.KindServer && !source.IsProxy(loader) {
		if f, ok := p.Files["eula.txt"]; !ok {
			add(Warning, "%s: no eula.txt; the server will refuse to start until eula=true is set (https://aka.ms/MinecraftEULA)", p.Name)
		} else if data, err := os.ReadFile(f.Abs); err == nil && !strings.Contains(strings.ReplaceAll(string(data), " ", ""), "eula=true") {
			add(Warning, "%s: eula.txt does not contain eula=true", p.Name)
		}
	}
	return out
}

func orUnknown(s string) string {
	if s == "" {
		return "(unknown)"
	}
	return s
}
