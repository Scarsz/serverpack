// Package source resolves jar descriptors against distribution channels
// (PaperMC, Modrinth, Hangar, Spigot, CurseForge, GitHub, Jenkins, Maven, URLs, …).
package source

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/Scarsz/serverpack/internal/descriptor"
	"github.com/Scarsz/serverpack/internal/httpx"
	"github.com/Scarsz/serverpack/internal/mcver"
)

// Context carries what a source needs to know about the server a jar is resolved for.
type Context struct {
	MCVersion string // "" when unknown (proxies, bases without a server); disables MC filtering
	Loader    string // server platform: paper, purpur, folia, velocity, waterfall, fabric, …
	HTTP      *httpx.Client
	Warn      func(format string, args ...any)
}

func (c *Context) warn(format string, args ...any) {
	if c.Warn != nil {
		c.Warn(format, args...)
	}
}

// ForDescriptor applies per-descriptor mc_version / loader overrides.
func (c *Context) ForDescriptor(d *descriptor.Descriptor) *Context {
	out := *c
	if d.MCVersion != "" {
		out.MCVersion = d.MCVersion
	}
	if d.Loader != "" {
		out.Loader = d.Loader
	}
	return &out
}

// Result is what a source resolved a descriptor to.
type Result struct {
	Version string
	URL     string
	Headers map[string]string
	SHA256  string
	SHA1    string
	SHA512  string
	MD5     string
	Size    int64
	Compat  descriptor.Compat
	// Build sources only: the pinned source commit. Nothing is downloaded for these at lock time.
	Revision string
	// Server sources only: the MC version and platform of the resolved server jar.
	MCVersion string
	Loader    string
}

// Source resolves descriptors for one distribution channel.
type Source interface {
	Resolve(ctx context.Context, d *descriptor.Descriptor, c *Context) (*Result, error)
}

var registry = map[string]Source{}

// Register adds a source under name.
func Register(name string, s Source) { registry[name] = s }

// Get looks up a source by name.
func Get(name string) (Source, error) {
	s, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown source %q (known: %s)", name, strings.Join(Names(), ", "))
	}
	return s, nil
}

// Names lists all registered sources.
func Names() []string {
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// ServerContext derives the MC version and platform a server descriptor provides.
func ServerContext(d *descriptor.Descriptor) (mc, loader string) {
	loader = d.Loader
	if loader == "" {
		switch d.Source {
		case "papermc":
			loader = d.Project
			if loader == "" {
				loader = "paper"
			}
		case "purpur", "fabric":
			loader = d.Source
		}
	}
	switch {
	case d.MCVersion != "":
		mc = d.MCVersion
	case d.Resolved != nil && d.Resolved.MCVersion != "":
		mc = d.Resolved.MCVersion
	case !d.IsFloating():
		mc = d.Version
	}
	if IsProxy(loader) {
		mc = ""
	}
	return mc, loader
}

// IsProxy reports whether a platform is a proxy (no Minecraft game version applies).
func IsProxy(loader string) bool {
	switch loader {
	case "velocity", "waterfall", "bungeecord":
		return true
	}
	return false
}

// CompatibleLoaders lists the plugin/mod platforms that run on a server platform.
// nil means unknown: no loader filtering is applied.
func CompatibleLoaders(server string) []string {
	switch strings.ToLower(server) {
	case "paper":
		return []string{"paper", "spigot", "bukkit"}
	case "purpur":
		return []string{"purpur", "paper", "spigot", "bukkit"}
	case "folia":
		return []string{"folia"}
	case "spigot":
		return []string{"spigot", "bukkit"}
	case "bukkit":
		return []string{"bukkit"}
	case "velocity":
		return []string{"velocity"}
	case "waterfall":
		return []string{"waterfall", "bungeecord"}
	case "bungeecord":
		return []string{"bungeecord"}
	case "fabric":
		return []string{"fabric"}
	case "quilt":
		return []string{"quilt", "fabric"}
	case "forge":
		return []string{"forge"}
	case "neoforge":
		return []string{"neoforge"}
	case "sponge":
		return []string{"sponge"}
	}
	return nil
}

// candidate is one release offered by a source, in a source-independent shape.
type candidate struct {
	Version    string
	Aliases    []string // other identifiers an exact pin may use (ids, file names)
	Channel    int
	MCVersions []string // supported MC versions; meaningful only if HasCompat
	HasCompat  bool
	Coarse     bool     // MCVersions are coarse lines like "1.21" (BukkitDev, Spigot)
	Loaders    []string // nil = unknown/any
	Payload    any
}

func (c *candidate) supports(mc string) descriptor.Compat {
	if mc == "" || !c.HasCompat {
		return descriptor.CompatUnknown
	}
	if mcver.MatchesAny(mc, c.MCVersions) {
		return descriptor.CompatYes
	}
	if c.Coarse {
		for _, v := range c.MCVersions {
			if mcver.SameLine(mc, v) {
				return descriptor.CompatYes
			}
		}
	}
	return descriptor.CompatNo
}

func (c *candidate) loaderOK(allowed []string) bool {
	if len(allowed) == 0 || len(c.Loaders) == 0 {
		return true
	}
	for _, l := range c.Loaders {
		if slices.Contains(allowed, strings.ToLower(l)) {
			return true
		}
	}
	return false
}

func (c *candidate) matchesExact(v string) bool {
	return c.Version == v || slices.Contains(c.Aliases, v)
}

// pick applies a descriptor's version selector to candidates ordered newest first.
func pick(name string, cands []candidate, d *descriptor.Descriptor, c *Context) (*candidate, descriptor.Compat, error) {
	rank, err := d.ChannelRank()
	if err != nil {
		return nil, "", err
	}
	loaders := CompatibleLoaders(c.Loader)
	sel := d.Selector()

	switch sel {
	case descriptor.Latest, descriptor.LatestAny:
		var newest *candidate
		for i := range cands {
			cand := &cands[i]
			if cand.Channel > rank || !cand.loaderOK(loaders) {
				continue
			}
			if newest == nil {
				newest = cand
			}
			compat := cand.supports(c.MCVersion)
			if sel == descriptor.LatestAny || compat != descriptor.CompatNo {
				return cand, compat, nil
			}
		}
		if newest == nil {
			return nil, "", fmt.Errorf("%s: no versions found%s", name, filterDesc(c, d))
		}
		return nil, "", fmt.Errorf("%s: no version supports Minecraft %s%s; newest is %s (supports %s). Pin an exact version, set mc_version, or use version = \"latest-any\"",
			name, c.MCVersion, filterDesc(c, d), newest.Version, summarize(newest.MCVersions))
	default:
		var fallback *candidate
		for i := range cands {
			cand := &cands[i]
			if !cand.matchesExact(sel) {
				continue
			}
			if cand.loaderOK(loaders) {
				return cand, cand.supports(c.MCVersion), nil
			}
			if fallback == nil {
				fallback = cand
			}
		}
		if fallback != nil {
			c.warn("%s: version %s does not declare support for %s; using it anyway", name, sel, c.Loader)
			return fallback, fallback.supports(c.MCVersion), nil
		}
		return nil, "", fmt.Errorf("%s: version %q not found", name, sel)
	}
}

func filterDesc(c *Context, d *descriptor.Descriptor) string {
	var parts []string
	if c.Loader != "" {
		parts = append(parts, "loader "+c.Loader)
	}
	ch := d.Channel
	if ch == "" {
		ch = "release"
	}
	parts = append(parts, "channel "+ch)
	return " (" + strings.Join(parts, ", ") + ")"
}

// summarize renders a supported-version list compactly ("1.20 … 1.21.8").
func summarize(vs []string) string {
	if len(vs) == 0 {
		return "no versions listed"
	}
	s := slices.Clone(vs)
	slices.SortFunc(s, mcver.Compare)
	if len(s) <= 4 {
		return strings.Join(s, ", ")
	}
	return s[0] + " … " + s[len(s)-1]
}

// channelFromName classifies free-form channel names (Hangar channels, build channels).
func channelFromName(name string) int {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "release"), strings.Contains(n, "stable"), strings.Contains(n, "recommended"):
		return descriptor.ChannelRelease
	case strings.Contains(n, "alpha"), strings.Contains(n, "dev"), strings.Contains(n, "nightly"), strings.Contains(n, "experimental"):
		return descriptor.ChannelAlpha
	}
	return descriptor.ChannelBeta
}

func require(name, field, value string) error {
	if value == "" {
		return fmt.Errorf("%s: field %q is required", name, field)
	}
	return nil
}
