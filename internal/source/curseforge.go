package source

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/Scarsz/serverpack/internal/descriptor"
)

func init() {
	key := func() string { return envFirst("CURSEFORGE_API_KEY", "CF_API_KEY") }
	Register("curseforge", &CurseForge{BaseURL: "https://api.curseforge.com/v1", Key: key})
	Register("bukkitdev", &CurseForge{BaseURL: "https://api.curseforge.com/v1", Key: key, ClassID: 5, Label: "bukkitdev"})
}

// CurseForge resolves CurseForge mods and BukkitDev plugins (CurseForge class 5) via the CurseForge Core API.
type CurseForge struct {
	BaseURL string
	Key     func() string
	ClassID int // 0 = any class
	Label   string
}

const curseforgeMinecraft = 432

type cfFile struct {
	ID           int      `json:"id"`
	DisplayName  string   `json:"displayName"`
	FileName     string   `json:"fileName"`
	ReleaseType  int      `json:"releaseType"`
	DownloadURL  string   `json:"downloadUrl"`
	GameVersions []string `json:"gameVersions"`
	FileLength   int64    `json:"fileLength"`
	IsAvailable  bool     `json:"isAvailable"`
	Hashes       []struct {
		Value string `json:"value"`
		Algo  int    `json:"algo"`
	} `json:"hashes"`
}

var cfLoaders = map[string]string{"forge": "forge", "fabric": "fabric", "quilt": "quilt", "neoforge": "neoforge"}

func (s *CurseForge) label() string {
	if s.Label != "" {
		return s.Label
	}
	return "curseforge"
}

func (s *CurseForge) Resolve(ctx context.Context, d *descriptor.Descriptor, c *Context) (*Result, error) {
	label := s.label() + ":" + d.Project
	if err := require(s.label(), "project", d.Project); err != nil {
		return nil, err
	}
	key := s.Key()
	if key == "" {
		return nil, fmt.Errorf("%s: set CURSEFORGE_API_KEY (get one at https://console.curseforge.com)", label)
	}
	headers := map[string]string{"x-api-key": key}

	modID := d.Project
	if !isDigits(modID) {
		q := url.Values{"gameId": {fmt.Sprint(curseforgeMinecraft)}, "slug": {d.Project}}
		if s.ClassID != 0 {
			q.Set("classId", fmt.Sprint(s.ClassID))
		}
		var resp struct {
			Data []struct {
				ID int `json:"id"`
			} `json:"data"`
		}
		if err := c.HTTP.GetJSON(ctx, s.BaseURL+"/mods/search?"+q.Encode(), headers, &resp); err != nil {
			return nil, err
		}
		if len(resp.Data) == 0 {
			return nil, fmt.Errorf("%s: project not found", label)
		}
		modID = fmt.Sprint(resp.Data[0].ID)
	}

	var cands []candidate
	const pageSize = 50
	for index := 0; index < 10*pageSize; index += pageSize {
		var resp struct {
			Data       []cfFile `json:"data"`
			Pagination struct {
				TotalCount int `json:"totalCount"`
			} `json:"pagination"`
		}
		u := fmt.Sprintf("%s/mods/%s/files?pageSize=%d&index=%d", s.BaseURL, modID, pageSize, index)
		if err := c.HTTP.GetJSON(ctx, u, headers, &resp); err != nil {
			return nil, err
		}
		for i := range resp.Data {
			f := &resp.Data[i]
			var mcs, loaders []string
			for _, gv := range f.GameVersions {
				if l, ok := cfLoaders[strings.ToLower(gv)]; ok {
					loaders = append(loaders, l)
				} else if gv != "" && (gv[0] >= '0' && gv[0] <= '9') {
					mcs = append(mcs, gv)
				}
			}
			cands = append(cands, candidate{
				Version:    f.DisplayName,
				Aliases:    []string{fmt.Sprint(f.ID), f.FileName},
				Channel:    f.ReleaseType - 1,
				MCVersions: mcs,
				HasCompat:  len(mcs) > 0,
				Coarse:     s.ClassID == 5,
				Loaders:    loaders,
				Payload:    f,
			})
		}
		if index+pageSize >= resp.Pagination.TotalCount {
			break
		}
	}
	cand, compat, err := pick(label, cands, d, c)
	if err != nil {
		return nil, err
	}
	f := cand.Payload.(*cfFile)
	if f.DownloadURL == "" {
		return nil, fmt.Errorf("%s: the author disallows third-party downloads of %s (file %d); download it yourself and use source = \"local\"", label, f.FileName, f.ID)
	}
	res := &Result{Version: f.DisplayName, URL: f.DownloadURL, Size: f.FileLength, Compat: compat}
	for _, h := range f.Hashes {
		switch h.Algo {
		case 1:
			res.SHA1 = h.Value
		case 2:
			res.MD5 = h.Value
		}
	}
	return res, nil
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
