package importer

import (
	"archive/zip"
	"bufio"
	"encoding/json"
	"io"
	"path/filepath"
	"regexp"
	"strings"
)

// jarInfo is what can be learned from inside a jar.
type jarInfo struct {
	Name      string // plugin/mod name from its metadata
	MCVersion string // paperclip version.json "id"
	Paperclip bool
}

func readJarInfo(path string) jarInfo {
	var info jarInfo
	zr, err := zip.OpenReader(path)
	if err != nil {
		return info
	}
	defer zr.Close()
	for _, f := range zr.File {
		switch f.Name {
		case "plugin.yml", "paper-plugin.yml", "bungee.yml":
			if info.Name == "" {
				info.Name = yamlName(f)
			}
		case "velocity-plugin.json", "fabric.mod.json", "quilt.mod.json":
			if info.Name == "" {
				info.Name = jsonName(f)
			}
		case "version.json":
			var v struct {
				ID string `json:"id"`
			}
			if readJSON(f, &v) == nil {
				info.MCVersion = v.ID
			}
		case "META-INF/MANIFEST.MF":
			if rc, err := f.Open(); err == nil {
				data, _ := io.ReadAll(io.LimitReader(rc, 64<<10))
				rc.Close()
				info.Paperclip = strings.Contains(string(data), "paperclip")
			}
		}
	}
	return info
}

func yamlName(f *zip.File) string {
	rc, err := f.Open()
	if err != nil {
		return ""
	}
	defer rc.Close()
	sc := bufio.NewScanner(rc)
	for sc.Scan() {
		line := sc.Text()
		if v, ok := strings.CutPrefix(line, "name:"); ok {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}

func jsonName(f *zip.File) string {
	var v struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if readJSON(f, &v) != nil {
		return ""
	}
	if v.Name != "" {
		return v.Name
	}
	return v.ID
}

func readJSON(f *zip.File, out any) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	return json.NewDecoder(io.LimitReader(rc, 1<<20)).Decode(out)
}

var versionSuffix = regexp.MustCompile(`(?i)[-_ .]*(v?\d+(\.\d+)*.*|SNAPSHOT.*|bukkit|paper|spigot)$`)
var unsafeName = regexp.MustCompile(`[^A-Za-z0-9_.+-]+`)

// descriptorName picks a stable descriptor name for a jar: its declared name, else the filename minus version.
func descriptorName(jarPath string, info jarInfo) string {
	name := info.Name
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(jarPath), filepath.Ext(jarPath))
		for {
			trimmed := versionSuffix.ReplaceAllString(name, "")
			if trimmed == name || trimmed == "" {
				break
			}
			name = trimmed
		}
	}
	name = unsafeName.ReplaceAllString(name, "-")
	name = strings.Trim(name, "-.")
	if name == "" {
		name = "plugin"
	}
	return name
}

// DescriptorName suggests a descriptor name for a jar file from its metadata or file name.
func DescriptorName(jarPath string) string {
	return descriptorName(jarPath, readJarInfo(jarPath))
}
