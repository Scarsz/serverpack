package source

import (
	"context"

	"fmt"
	"net/url"
	"os"
	"path"
	"strings"

	"github.com/Scarsz/serverpack/internal/descriptor"
	"github.com/Scarsz/serverpack/internal/httpx"
)

func init() {
	Register("github", &GitHub{BaseURL: "https://api.github.com"})
	Register("jenkins", &Jenkins{})
	Register("maven", &Maven{})
	Register("url", &URL{})
	Register("local", &Local{})
}

func envFirst(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

// matchOne returns the single name matching glob, erroring on zero or several matches.
func matchOne(label, glob string, names []string) (int, error) {
	if glob == "" {
		glob = "*.jar"
	}
	var hits []int
	for i, n := range names {
		if ok, _ := path.Match(glob, n); ok {
			hits = append(hits, i)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return 0, fmt.Errorf("%s: no file matches asset %q (available: %s)", label, glob, strings.Join(names, ", "))
	}
	var matched []string
	for _, i := range hits {
		matched = append(matched, names[i])
	}
	return 0, fmt.Errorf("%s: asset %q matches several files (%s); set asset to a narrower glob", label, glob, strings.Join(matched, ", "))
}

// GitHub resolves release assets.
type GitHub struct{ BaseURL string }

type ghRelease struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name               string `json:"name"`
		Size               int64  `json:"size"`
		Digest             string `json:"digest"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

func (s *GitHub) Resolve(ctx context.Context, d *descriptor.Descriptor, c *Context) (*Result, error) {
	if err := require("github", "project", d.Project); err != nil {
		return nil, err
	}
	label := "github:" + d.Project
	rank, err := d.ChannelRank()
	if err != nil {
		return nil, err
	}
	var headers map[string]string
	if tok := envFirst("GITHUB_TOKEN", "GH_TOKEN"); tok != "" {
		headers = map[string]string{"Authorization": "Bearer " + tok}
	}
	base := s.BaseURL + "/repos/" + d.Project + "/releases"

	var rel ghRelease
	switch sel := d.Selector(); {
	case (sel == descriptor.Latest || sel == descriptor.LatestAny) && rank == descriptor.ChannelRelease:
		if err := c.HTTP.GetJSON(ctx, base+"/latest", headers, &rel); err != nil {
			if httpx.IsNotFound(err) {
				return nil, fmt.Errorf("%s: no releases found (or repository is private; set GITHUB_TOKEN)", label)
			}
			return nil, err
		}
	case sel == descriptor.Latest || sel == descriptor.LatestAny:
		var rels []ghRelease
		if err := c.HTTP.GetJSON(ctx, base+"?per_page=30", headers, &rels); err != nil {
			return nil, err
		}
		for _, r := range rels {
			if !r.Draft {
				rel = r
				break
			}
		}
		if rel.TagName == "" {
			return nil, fmt.Errorf("%s: no releases found", label)
		}
	default:
		err := c.HTTP.GetJSON(ctx, base+"/tags/"+url.PathEscape(sel), headers, &rel)
		if httpx.IsNotFound(err) && !strings.HasPrefix(sel, "v") {
			err = c.HTTP.GetJSON(ctx, base+"/tags/"+url.PathEscape("v"+sel), headers, &rel)
		}
		if err != nil {
			if httpx.IsNotFound(err) {
				return nil, fmt.Errorf("%s: release %q not found", label, sel)
			}
			return nil, err
		}
	}

	names := make([]string, len(rel.Assets))
	for i, a := range rel.Assets {
		names[i] = a.Name
	}
	i, err := matchOne(label+"@"+rel.TagName, d.Asset, names)
	if err != nil {
		return nil, err
	}
	a := rel.Assets[i]
	res := &Result{Version: rel.TagName, URL: a.BrowserDownloadURL, Size: a.Size, Compat: descriptor.CompatUnknown}
	if sha, ok := strings.CutPrefix(a.Digest, "sha256:"); ok {
		res.SHA256 = sha
	}
	return res, nil
}

// Jenkins resolves build artifacts from a Jenkins job.
type Jenkins struct{}

func (s *Jenkins) Resolve(ctx context.Context, d *descriptor.Descriptor, c *Context) (*Result, error) {
	if err := require("jenkins", "url", d.URL); err != nil {
		return nil, err
	}
	job := strings.TrimSuffix(d.URL, "/")
	label := "jenkins:" + job
	build := d.Selector()
	if build == descriptor.Latest || build == descriptor.LatestAny {
		build = "lastSuccessfulBuild"
	}
	var info struct {
		Number    int `json:"number"`
		Artifacts []struct {
			FileName     string `json:"fileName"`
			RelativePath string `json:"relativePath"`
		} `json:"artifacts"`
	}
	u := job + "/" + url.PathEscape(build) + "/api/json?tree=" + url.QueryEscape("number,artifacts[fileName,relativePath]")
	if err := c.HTTP.GetJSON(ctx, u, nil, &info); err != nil {
		return nil, err
	}
	names := make([]string, len(info.Artifacts))
	for i, a := range info.Artifacts {
		names[i] = a.FileName
	}
	i, err := matchOne(fmt.Sprintf("%s#%d", label, info.Number), d.Asset, names)
	if err != nil {
		return nil, err
	}
	return &Result{
		Version: fmt.Sprint(info.Number),
		URL:     fmt.Sprintf("%s/%d/artifact/%s", job, info.Number, info.Artifacts[i].RelativePath),
		Compat:  descriptor.CompatUnknown,
	}, nil
}

// Maven resolves artifacts from a Maven repository.
type Maven struct{}

type mavenMetadata struct {
	Versioning struct {
		Latest   string   `xml:"latest"`
		Release  string   `xml:"release"`
		Versions []string `xml:"versions>version"`
		Snapshot struct {
			Timestamp   string `xml:"timestamp"`
			BuildNumber string `xml:"buildNumber"`
		} `xml:"snapshot"`
		SnapshotVersions []struct {
			Classifier string `xml:"classifier"`
			Extension  string `xml:"extension"`
			Value      string `xml:"value"`
		} `xml:"snapshotVersions>snapshotVersion"`
	} `xml:"versioning"`
}

func (s *Maven) Resolve(ctx context.Context, d *descriptor.Descriptor, c *Context) (*Result, error) {
	if err := require("maven", "repo", d.Repo); err != nil {
		return nil, err
	}
	if err := require("maven", "project", d.Project); err != nil {
		return nil, err
	}
	parts := strings.Split(d.Project, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return nil, fmt.Errorf("maven: project must be group:artifact[:classifier], got %q", d.Project)
	}
	group, artifact := parts[0], parts[1]
	classifier := d.Classifier
	if len(parts) == 3 && classifier == "" {
		classifier = parts[2]
	}
	label := "maven:" + d.Project
	rank, err := d.ChannelRank()
	if err != nil {
		return nil, err
	}
	base := strings.TrimSuffix(d.Repo, "/") + "/" + strings.ReplaceAll(group, ".", "/") + "/" + artifact

	v := d.Selector()
	if v == descriptor.Latest || v == descriptor.LatestAny {
		var md mavenMetadata
		if err := c.HTTP.GetXML(ctx, base+"/maven-metadata.xml", nil, &md); err != nil {
			return nil, err
		}
		if rank == descriptor.ChannelRelease {
			v = md.Versioning.Release
			if v == "" {
				for i := len(md.Versioning.Versions) - 1; i >= 0; i-- {
					if !strings.HasSuffix(md.Versioning.Versions[i], "-SNAPSHOT") {
						v = md.Versioning.Versions[i]
						break
					}
				}
			}
		} else {
			v = md.Versioning.Latest
			if v == "" && len(md.Versioning.Versions) > 0 {
				v = md.Versioning.Versions[len(md.Versioning.Versions)-1]
			}
		}
		if v == "" {
			return nil, fmt.Errorf("%s: no versions in maven-metadata.xml", label)
		}
	}

	fileVersion := v
	if strings.HasSuffix(v, "-SNAPSHOT") {
		var md mavenMetadata
		if err := c.HTTP.GetXML(ctx, base+"/"+v+"/maven-metadata.xml", nil, &md); err != nil {
			return nil, err
		}
		for _, sv := range md.Versioning.SnapshotVersions {
			if sv.Extension == "jar" && sv.Classifier == classifier {
				fileVersion = sv.Value
				break
			}
		}
		if fileVersion == v && md.Versioning.Snapshot.Timestamp != "" {
			fileVersion = strings.TrimSuffix(v, "-SNAPSHOT") + "-" + md.Versioning.Snapshot.Timestamp + "-" + md.Versioning.Snapshot.BuildNumber
		}
	}
	file := artifact + "-" + fileVersion
	if classifier != "" {
		file += "-" + classifier
	}
	return &Result{
		Version: fileVersion,
		URL:     base + "/" + v + "/" + file + ".jar",
		Compat:  descriptor.CompatUnknown,
	}, nil
}

// URL downloads a fixed URL. The sha256 is pinned at lock time (or verified if given).
type URL struct{}

func (s *URL) Resolve(ctx context.Context, d *descriptor.Descriptor, c *Context) (*Result, error) {
	if err := require("url", "url", d.URL); err != nil {
		return nil, err
	}
	v := d.Version
	if v == "" || v == descriptor.Latest || v == descriptor.LatestAny {
		v = "static"
	}
	return &Result{Version: v, URL: d.URL, SHA256: d.SHA256, Compat: descriptor.CompatUnknown}, nil
}

// Local refers to a jar committed next to its descriptor. It never needs resolving.
type Local struct{}

func (s *Local) Resolve(ctx context.Context, d *descriptor.Descriptor, c *Context) (*Result, error) {
	return nil, fmt.Errorf("local descriptors are not resolved")
}
