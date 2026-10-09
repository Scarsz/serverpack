package source

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/Scarsz/serverpack/internal/descriptor"
	"github.com/Scarsz/serverpack/internal/mcver"
)

func init() {
	Register("papermc", &PaperMC{BaseURL: "https://fill.papermc.io/v3"})
	Register("purpur", &Purpur{BaseURL: "https://api.purpurmc.org/v2"})
	Register("fabric", &Fabric{BaseURL: "https://meta.fabricmc.net/v2"})
}

// PaperMC resolves Paper, Folia, Velocity and Waterfall through the Fill v3 API.
type PaperMC struct{ BaseURL string }

type fillBuild struct {
	ID        int    `json:"id"`
	Channel   string `json:"channel"`
	Downloads map[string]struct {
		Name      string `json:"name"`
		Checksums struct {
			SHA256 string `json:"sha256"`
		} `json:"checksums"`
		Size int64  `json:"size"`
		URL  string `json:"url"`
	} `json:"downloads"`
}

func (s *PaperMC) Resolve(ctx context.Context, d *descriptor.Descriptor, c *Context) (*Result, error) {
	project := d.Project
	if project == "" {
		project = "paper"
	}
	rank, err := d.ChannelRank()
	if err != nil {
		return nil, err
	}
	base := s.BaseURL + "/projects/" + url.PathEscape(project)

	mc := d.Selector()
	if mc == descriptor.Latest || mc == descriptor.LatestAny {
		var resp struct {
			Versions []struct {
				Version struct {
					ID string `json:"id"`
				} `json:"version"`
				Builds []int `json:"builds"`
			} `json:"versions"`
		}
		if err := c.HTTP.GetJSON(ctx, base+"/versions", nil, &resp); err != nil {
			return nil, err
		}
		var ids []string
		for _, v := range resp.Versions {
			if len(v.Builds) > 0 {
				ids = append(ids, v.Version.ID)
			}
		}
		mc = newestVersion(ids, rank)
		if mc == "" {
			return nil, fmt.Errorf("papermc: project %q has no versions with builds", project)
		}
	}

	var build fillBuild
	switch b := d.Build; b {
	case "", descriptor.Latest:
		var builds []fillBuild
		if err := c.HTTP.GetJSON(ctx, base+"/versions/"+url.PathEscape(mc)+"/builds", nil, &builds); err != nil {
			return nil, err
		}
		sort.Slice(builds, func(i, j int) bool { return builds[i].ID > builds[j].ID })
		found := false
		for _, cand := range builds {
			if channelFromName(cand.Channel) <= rank {
				build, found = cand, true
				break
			}
		}
		if !found {
			if len(builds) == 0 {
				return nil, fmt.Errorf("papermc: %s %s has no builds", project, mc)
			}
			return nil, fmt.Errorf("papermc: %s %s has no builds on the %q channel (newest build %d is %s); set channel = \"beta\" or \"alpha\"",
				project, mc, orDefault(d.Channel, "release"), builds[0].ID, builds[0].Channel)
		}
	default:
		if err := c.HTTP.GetJSON(ctx, base+"/versions/"+url.PathEscape(mc)+"/builds/"+url.PathEscape(b), nil, &build); err != nil {
			return nil, err
		}
	}

	dl, ok := build.Downloads["server:default"]
	if !ok {
		return nil, fmt.Errorf("papermc: %s %s build %d has no server:default download", project, mc, build.ID)
	}
	res := &Result{
		Version:   fmt.Sprintf("%s-%d", mc, build.ID),
		URL:       dl.URL,
		SHA256:    dl.Checksums.SHA256,
		Size:      dl.Size,
		Compat:    descriptor.CompatYes,
		MCVersion: mc,
		Loader:    project,
	}
	if IsProxy(project) {
		res.MCVersion = "" // proxy versions aren't Minecraft versions
	}
	return res, nil
}

// newestVersion returns the first (newest) id that is a plain release, falling back to the first
// id at all when the channel allows pre-releases or nothing is a release (e.g. Velocity snapshots).
func newestVersion(newestFirst []string, rank int) string {
	if len(newestFirst) == 0 {
		return ""
	}
	if rank == descriptor.ChannelRelease {
		for _, v := range newestFirst {
			if mcver.IsRelease(v) {
				return v
			}
		}
	}
	return newestFirst[0]
}

// Purpur resolves Purpur server jars.
type Purpur struct{ BaseURL string }

func (s *Purpur) Resolve(ctx context.Context, d *descriptor.Descriptor, c *Context) (*Result, error) {
	rank, err := d.ChannelRank()
	if err != nil {
		return nil, err
	}
	mc := d.Selector()
	if mc == descriptor.Latest || mc == descriptor.LatestAny {
		var resp struct {
			Metadata struct {
				Current string `json:"current"`
			} `json:"metadata"`
			Versions []string `json:"versions"`
		}
		if err := c.HTTP.GetJSON(ctx, s.BaseURL+"/purpur", nil, &resp); err != nil {
			return nil, err
		}
		mc = resp.Metadata.Current
		if mc == "" {
			sort.Slice(resp.Versions, func(i, j int) bool { return mcver.Compare(resp.Versions[i], resp.Versions[j]) > 0 })
			mc = newestVersion(resp.Versions, rank)
		}
	}
	build := d.Build
	if build == "" {
		build = descriptor.Latest
	}
	var info struct {
		Build  string `json:"build"`
		Result string `json:"result"`
		MD5    string `json:"md5"`
	}
	if err := c.HTTP.GetJSON(ctx, s.BaseURL+"/purpur/"+url.PathEscape(mc)+"/"+url.PathEscape(build), nil, &info); err != nil {
		return nil, err
	}
	if info.Result != "" && info.Result != "SUCCESS" {
		return nil, fmt.Errorf("purpur: %s build %s did not succeed (%s)", mc, info.Build, info.Result)
	}
	return &Result{
		Version:   mc + "-" + info.Build,
		URL:       s.BaseURL + "/purpur/" + url.PathEscape(mc) + "/" + url.PathEscape(info.Build) + "/download",
		MD5:       info.MD5,
		Compat:    descriptor.CompatYes,
		MCVersion: mc,
		Loader:    "purpur",
	}, nil
}

// Fabric resolves the Fabric server launcher jar from Fabric meta.
type Fabric struct{ BaseURL string }

type fabricVersion struct {
	Version string `json:"version"`
	Stable  bool   `json:"stable"`
}

func (s *Fabric) Resolve(ctx context.Context, d *descriptor.Descriptor, c *Context) (*Result, error) {
	rank, err := d.ChannelRank()
	if err != nil {
		return nil, err
	}
	pickStable := func(vs []fabricVersion, what string) (string, error) {
		for _, v := range vs {
			if v.Stable || rank > descriptor.ChannelRelease {
				return v.Version, nil
			}
		}
		return "", fmt.Errorf("fabric: no stable %s version found", what)
	}

	mc := d.Selector()
	if mc == descriptor.Latest || mc == descriptor.LatestAny {
		var games []fabricVersion
		if err := c.HTTP.GetJSON(ctx, s.BaseURL+"/versions/game", nil, &games); err != nil {
			return nil, err
		}
		if mc, err = pickStable(games, "game"); err != nil {
			return nil, err
		}
	}
	loader := d.LoaderVersion
	if loader == "" || loader == descriptor.Latest {
		var loaders []struct {
			Loader fabricVersion `json:"loader"`
		}
		if err := c.HTTP.GetJSON(ctx, s.BaseURL+"/versions/loader/"+url.PathEscape(mc), nil, &loaders); err != nil {
			return nil, err
		}
		vs := make([]fabricVersion, len(loaders))
		for i, l := range loaders {
			vs[i] = l.Loader
		}
		if loader, err = pickStable(vs, "loader"); err != nil {
			return nil, fmt.Errorf("%w for Minecraft %s", err, mc)
		}
	}
	installer := d.InstallerVersion
	if installer == "" || installer == descriptor.Latest {
		var installers []fabricVersion
		if err := c.HTTP.GetJSON(ctx, s.BaseURL+"/versions/installer", nil, &installers); err != nil {
			return nil, err
		}
		if installer, err = pickStable(installers, "installer"); err != nil {
			return nil, err
		}
	}
	return &Result{
		Version:   strings.Join([]string{mc, loader, installer}, "-"),
		URL:       fmt.Sprintf("%s/versions/loader/%s/%s/%s/server/jar", s.BaseURL, url.PathEscape(mc), url.PathEscape(loader), url.PathEscape(installer)),
		Compat:    descriptor.CompatYes,
		MCVersion: mc,
		Loader:    "fabric",
	}, nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
