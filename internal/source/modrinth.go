package source

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"time"

	"github.com/Scarsz/serverpack/internal/descriptor"
	"github.com/Scarsz/serverpack/internal/httpx"
)

func init() {
	Register("modrinth", &Modrinth{BaseURL: "https://api.modrinth.com/v2"})
}

// Modrinth resolves projects from modrinth.com.
type Modrinth struct{ BaseURL string }

// ModrinthVersion is the subset of Modrinth's version object serverpack uses.
type ModrinthVersion struct {
	ID            string    `json:"id"`
	ProjectID     string    `json:"project_id"`
	VersionNumber string    `json:"version_number"`
	VersionType   string    `json:"version_type"`
	GameVersions  []string  `json:"game_versions"`
	Loaders       []string  `json:"loaders"`
	DatePublished time.Time `json:"date_published"`
	Files         []struct {
		URL      string `json:"url"`
		Filename string `json:"filename"`
		Primary  bool   `json:"primary"`
		Size     int64  `json:"size"`
		Hashes   struct {
			SHA1   string `json:"sha1"`
			SHA512 string `json:"sha512"`
		} `json:"hashes"`
	} `json:"files"`
}

func (v *ModrinthVersion) primaryFile() (int, bool) {
	for i, f := range v.Files {
		if f.Primary {
			return i, true
		}
	}
	return 0, len(v.Files) > 0
}

func (s *Modrinth) versions(ctx context.Context, h *httpx.Client, project string, loaders, gameVersions []string) ([]ModrinthVersion, error) {
	q := url.Values{}
	if len(loaders) > 0 {
		b, _ := json.Marshal(loaders)
		q.Set("loaders", string(b))
	}
	if len(gameVersions) > 0 {
		b, _ := json.Marshal(gameVersions)
		q.Set("game_versions", string(b))
	}
	u := s.BaseURL + "/project/" + url.PathEscape(project) + "/version"
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var out []ModrinthVersion
	if err := h.GetJSON(ctx, u, nil, &out); err != nil {
		if httpx.IsNotFound(err) {
			return nil, fmt.Errorf("modrinth: project %q not found", project)
		}
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].DatePublished.After(out[j].DatePublished) })
	return out, nil
}

func modrinthChannel(t string) int {
	switch t {
	case "beta":
		return descriptor.ChannelBeta
	case "alpha":
		return descriptor.ChannelAlpha
	}
	return descriptor.ChannelRelease
}

func (s *Modrinth) Resolve(ctx context.Context, d *descriptor.Descriptor, c *Context) (*Result, error) {
	if err := require("modrinth", "project", d.Project); err != nil {
		return nil, err
	}
	loaders := CompatibleLoaders(c.Loader)
	var gameVersions []string
	if d.Selector() == descriptor.Latest && c.MCVersion != "" {
		gameVersions = []string{c.MCVersion}
	}
	vs, err := s.versions(ctx, c.HTTP, d.Project, loaders, gameVersions)
	if err != nil {
		return nil, err
	}
	if len(vs) == 0 && gameVersions != nil {
		// Nothing for this MC version: refetch without the filter so the error can name the newest version.
		if vs, err = s.versions(ctx, c.HTTP, d.Project, loaders, nil); err != nil {
			return nil, err
		}
	}
	cands := make([]candidate, 0, len(vs))
	for i := range vs {
		v := &vs[i]
		cands = append(cands, candidate{
			Version:    v.VersionNumber,
			Aliases:    []string{v.ID},
			Channel:    modrinthChannel(v.VersionType),
			MCVersions: v.GameVersions,
			HasCompat:  true,
			Loaders:    v.Loaders,
			Payload:    v,
		})
	}
	cand, compat, err := pick("modrinth:"+d.Project, cands, d, c)
	if err != nil {
		return nil, err
	}
	v := cand.Payload.(*ModrinthVersion)
	fi, ok := v.primaryFile()
	if !ok {
		return nil, fmt.Errorf("modrinth: %s %s has no files", d.Project, v.VersionNumber)
	}
	f := v.Files[fi]
	return &Result{
		Version: v.VersionNumber,
		URL:     f.URL,
		SHA1:    f.Hashes.SHA1,
		SHA512:  f.Hashes.SHA512,
		Size:    f.Size,
		Compat:  compat,
	}, nil
}

// ModrinthMatch is a jar identified by hash.
type ModrinthMatch struct {
	ProjectSlug string
	Version     ModrinthVersion
}

// LookupHashes identifies jars by sha1 via Modrinth's version_files endpoint.
func (s *Modrinth) LookupHashes(ctx context.Context, h *httpx.Client, sha1s []string) (map[string]ModrinthMatch, error) {
	if len(sha1s) == 0 {
		return nil, nil
	}
	var byHash map[string]ModrinthVersion
	body := map[string]any{"hashes": sha1s, "algorithm": "sha1"}
	if err := h.PostJSON(ctx, s.BaseURL+"/version_files", body, &byHash); err != nil {
		return nil, err
	}
	ids := map[string]bool{}
	for _, v := range byHash {
		ids[v.ProjectID] = true
	}
	idList := make([]string, 0, len(ids))
	for id := range ids {
		idList = append(idList, id)
	}
	slugs := map[string]string{}
	if len(idList) > 0 {
		b, _ := json.Marshal(idList)
		var projects []struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
		}
		if err := h.GetJSON(ctx, s.BaseURL+"/projects?ids="+url.QueryEscape(string(b)), nil, &projects); err != nil {
			return nil, err
		}
		for _, p := range projects {
			slugs[p.ID] = p.Slug
		}
	}
	out := map[string]ModrinthMatch{}
	for hash, v := range byHash {
		slug := slugs[v.ProjectID]
		if slug == "" {
			slug = v.ProjectID
		}
		out[hash] = ModrinthMatch{ProjectSlug: slug, Version: v}
	}
	return out, nil
}
