package source

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/Scarsz/serverpack/internal/descriptor"
	"github.com/Scarsz/serverpack/internal/httpx"
)

func init() {
	Register("hangar", &Hangar{BaseURL: "https://hangar.papermc.io/api/v1"})
}

// Hangar resolves projects from hangar.papermc.io.
type Hangar struct{ BaseURL string }

type hangarVersion struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	Channel   struct {
		Name string `json:"name"`
	} `json:"channel"`
	Downloads map[string]struct {
		FileInfo *struct {
			Name       string `json:"name"`
			SizeBytes  int64  `json:"sizeBytes"`
			SHA256Hash string `json:"sha256Hash"`
		} `json:"fileInfo"`
		ExternalURL string `json:"externalUrl"`
		DownloadURL string `json:"downloadUrl"`
	} `json:"downloads"`
	PlatformDependencies map[string][]string `json:"platformDependencies"`
}

// hangarPlatform maps a server platform to Hangar's platform enum.
func hangarPlatform(loader string) string {
	switch strings.ToLower(loader) {
	case "velocity":
		return "VELOCITY"
	case "waterfall", "bungeecord":
		return "WATERFALL"
	}
	return "PAPER"
}

const hangarPageSize = 25
const hangarMaxPages = 8

func (s *Hangar) Resolve(ctx context.Context, d *descriptor.Descriptor, c *Context) (*Result, error) {
	if err := require("hangar", "project", d.Project); err != nil {
		return nil, err
	}
	// Accept "Owner/Slug" (as shown in Hangar URLs) as well as the bare slug.
	slug := d.Project
	if i := strings.LastIndex(slug, "/"); i >= 0 {
		slug = slug[i+1:]
	}
	platform := hangarPlatform(c.Loader)
	label := "hangar:" + d.Project
	base := s.BaseURL + "/projects/" + url.PathEscape(slug) + "/versions"

	toCandidate := func(v *hangarVersion) (candidate, bool) {
		if _, ok := v.Downloads[platform]; !ok {
			return candidate{}, false
		}
		return candidate{
			Version:    v.Name,
			Channel:    channelFromName(v.Channel.Name),
			MCVersions: v.PlatformDependencies[platform],
			HasCompat:  platform == "PAPER",
			Payload:    v,
		}, true
	}

	var chosen *hangarVersion
	var compat descriptor.Compat
	if sel := d.Selector(); sel != descriptor.Latest && sel != descriptor.LatestAny {
		var v hangarVersion
		if err := c.HTTP.GetJSON(ctx, base+"/"+url.PathEscape(sel), nil, &v); err != nil {
			if httpx.IsNotFound(err) {
				return nil, fmt.Errorf("%s: version %q not found", label, sel)
			}
			return nil, err
		}
		cand, ok := toCandidate(&v)
		if !ok {
			return nil, fmt.Errorf("%s: version %s has no %s download", label, sel, platform)
		}
		chosen, compat = &v, cand.supports(c.MCVersion)
	} else {
		var cands []candidate
		var lastErr error
		for page := 0; page < hangarMaxPages; page++ {
			q := url.Values{}
			q.Set("limit", fmt.Sprint(hangarPageSize))
			q.Set("offset", fmt.Sprint(page*hangarPageSize))
			q.Set("platform", platform)
			var resp struct {
				Pagination struct {
					Count int `json:"count"`
				} `json:"pagination"`
				Result []hangarVersion `json:"result"`
			}
			if err := c.HTTP.GetJSON(ctx, base+"?"+q.Encode(), nil, &resp); err != nil {
				if httpx.IsNotFound(err) {
					return nil, fmt.Errorf("%s: project not found", label)
				}
				return nil, err
			}
			for i := range resp.Result {
				if cand, ok := toCandidate(&resp.Result[i]); ok {
					cands = append(cands, cand)
				}
			}
			cand, cmp, err := pick(label, cands, d, c)
			if err == nil {
				chosen, compat = cand.Payload.(*hangarVersion), cmp
				break
			}
			lastErr = err
			if (page+1)*hangarPageSize >= resp.Pagination.Count {
				break
			}
		}
		if chosen == nil {
			return nil, lastErr
		}
	}

	dl := chosen.Downloads[platform]
	res := &Result{Version: chosen.Name, Compat: compat}
	switch {
	case dl.DownloadURL != "":
		res.URL = dl.DownloadURL
		if dl.FileInfo != nil {
			res.SHA256, res.Size = dl.FileInfo.SHA256Hash, dl.FileInfo.SizeBytes
		}
	case dl.ExternalURL != "":
		res.URL = dl.ExternalURL
		c.warn("%s %s is hosted externally (%s); its checksum is computed at lock time", label, chosen.Name, dl.ExternalURL)
	default:
		return nil, fmt.Errorf("%s: version %s has no download URL", label, chosen.Name)
	}
	return res, nil
}
