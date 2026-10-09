package source

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/Scarsz/serverpack/internal/descriptor"
	"github.com/Scarsz/serverpack/internal/httpx"
)

func init() {
	s := &Spigot{BaseURL: "https://api.spiget.org/v2"}
	Register("spigot", s)
	Register("spiget", s)
}

// Spigot resolves SpigotMC resources through the Spiget API.
type Spigot struct{ BaseURL string }

var trailingID = regexp.MustCompile(`(\d+)/?$`)

// spigotID accepts "12345", "essentialsx.9089" or a full resource URL.
func spigotID(project string) (string, error) {
	m := trailingID.FindStringSubmatch(strings.TrimSpace(project))
	if m == nil {
		return "", fmt.Errorf("spigot: project must be a resource id (e.g. 9089 or essentialsx.9089), got %q", project)
	}
	return m[1], nil
}

func (s *Spigot) Resolve(ctx context.Context, d *descriptor.Descriptor, c *Context) (*Result, error) {
	if err := require("spigot", "project", d.Project); err != nil {
		return nil, err
	}
	id, err := spigotID(d.Project)
	if err != nil {
		return nil, err
	}
	label := "spigot:" + d.Project
	base := s.BaseURL + "/resources/" + id

	var res struct {
		Name     string `json:"name"`
		Premium  bool   `json:"premium"`
		External bool   `json:"external"`
		File     struct {
			Type        string `json:"type"`
			ExternalURL string `json:"externalUrl"`
		} `json:"file"`
		TestedVersions []string `json:"testedVersions"`
	}
	if err := c.HTTP.GetJSON(ctx, base, nil, &res); err != nil {
		if httpx.IsNotFound(err) {
			return nil, fmt.Errorf("%s: resource not found", label)
		}
		return nil, err
	}
	if res.Premium {
		return nil, fmt.Errorf("%s: %s is a premium resource and can't be downloaded automatically; download it yourself and use source = \"local\"", label, res.Name)
	}
	if res.External {
		return nil, fmt.Errorf("%s: %s is hosted externally (%s); use the github/url/jenkins source instead", label, res.Name, res.File.ExternalURL)
	}

	// Spiget can only serve the current version (from its CDN); per-version downloads redirect to
	// spigotmc.org, which blocks automated clients. Exact pins therefore must name the latest version.
	var latest struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	if err := c.HTTP.GetJSON(ctx, base+"/versions/latest", nil, &latest); err != nil {
		return nil, err
	}
	if sel := d.Selector(); sel != descriptor.Latest && sel != descriptor.LatestAny && sel != latest.Name && sel != fmt.Sprint(latest.ID) {
		return nil, fmt.Errorf("%s: Spigot only allows downloading the latest version (%s), not %q; use version = \"latest\", or download it yourself and use source = \"local\"", label, latest.Name, sel)
	}

	// Spigot only tracks coarse tested versions per resource, so compatibility is a hint, never a hard filter.
	cand := candidate{MCVersions: res.TestedVersions, HasCompat: len(res.TestedVersions) > 0, Coarse: true}
	compat := cand.supports(c.MCVersion)
	if compat == descriptor.CompatNo {
		c.warn("%s: %s lists tested versions %s, which don't include %s", label, res.Name, summarize(res.TestedVersions), c.MCVersion)
	}
	return &Result{
		Version: latest.Name,
		URL:     base + "/download",
		Compat:  compat,
	}, nil
}
