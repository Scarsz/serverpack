package cli

import (
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Scarsz/serverpack/internal/descriptor"
	"github.com/Scarsz/serverpack/internal/importer"
	"github.com/Scarsz/serverpack/internal/pack"
	"github.com/Scarsz/serverpack/internal/resolve"
	"github.com/Scarsz/serverpack/internal/source"
)

func (a *app) rootOrCwd() (string, error) {
	if root, err := a.root(); err == nil {
		return root, nil
	}
	return os.Getwd()
}

func (a *app) initCommand() *cobra.Command {
	var (
		base       bool
		extends    []string
		src        string
		project    string
		mcVersion  string
		image      string
		acceptEULA bool
	)
	cmd := &cobra.Command{
		Use:   "init <name>",
		Short: "Scaffold a new server (or base, with --base)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := a.rootOrCwd()
			if err != nil {
				return err
			}
			kind := pack.KindServer
			if base {
				kind = pack.KindBase
			}
			dir := pack.Dir(root, kind, args[0])
			if _, err := os.Stat(filepath.Join(dir, pack.ManifestFile)); err == nil {
				return fmt.Errorf("%s already exists", filepath.Join(dir, pack.ManifestFile))
			}
			if err := os.MkdirAll(filepath.Join(dir, "plugins"), 0o755); err != nil {
				return err
			}
			var b strings.Builder
			if !base {
				fmt.Fprintf(&b, "name = %q\n", args[0])
			}
			if len(extends) > 0 {
				fmt.Fprintf(&b, "extends = [%s]\n", quoteList(extends))
			} else {
				fmt.Fprintf(&b, "# extends = [\"common\"]\n")
			}
			if base {
				b.WriteString(`
# Anything set here is inherited by servers that extend this base.
# [server]               # a shared server jar; servers may override the whole table
# source = "papermc"
# project = "paper"
# version = "1.21.10"
#
# [runtime]
# image = "eclipse-temurin:25"
# jvm_flags = []
#
# [build]
# images = ["registry.example.com/{name}"]
# tags = ["latest", "{git_sha}"]
#
# [volumes]
# paths = ["logs"]
`)
			} else if len(extends) > 0 {
				// Inherit as much as possible: empty lists here would replace the bases' lists.
				inherited := false
				for _, name := range extends {
					bp, err := pack.LoadKind(root, pack.KindBase, name)
					if err != nil {
						return fmt.Errorf("--extends %s: %w", name, err)
					}
					inherited = inherited || bp.Manifest.Server != nil
				}
				f := cmd.Flags()
				if !inherited || f.Changed("source") || f.Changed("project") || f.Changed("mc-version") {
					b.WriteString("\n")
					b.Write(descriptor.MarshalTable(pack.ServerTable, &descriptor.Descriptor{Source: src, Project: project, Version: mcVersion}))
				} else {
					fmt.Fprintf(&b, "\n# [server] is inherited from %s; define it here to use a different server jar.\n", strings.Join(extends, ", "))
				}
				b.WriteString("\n[runtime]\n")
				if f.Changed("image") {
					fmt.Fprintf(&b, "image = %q\n", image)
				}
				b.WriteString(`# Everything else is inherited. Lists set here replace the inherited ones;
# use jvm_flags_append / images_append / tags_append to add to them instead.
# memory = { min = "1G", max = "4G" }
# jvm_flags_append = []
#
# [volumes]               # merged with the bases' volume paths
# paths = []
#
# [templates]             # merged with the bases' template globs
# include = []
`)
			} else {
				b.WriteString("\n")
				b.Write(descriptor.MarshalTable(pack.ServerTable, &descriptor.Descriptor{Source: src, Project: project, Version: mcVersion}))
				fmt.Fprintf(&b, `
[runtime]
image = %q
memory = { min = "1G", max = "4G" }
jvm_flags = []
args = []

[build]
# images = ["registry.example.com/{name}"]
# tags = ["latest", "{git_sha}", "{mc_version}"]

[volumes]
# Runtime state under /data: excluded from the image, mount these.
paths = ["logs"]

[templates]
# Files rendered with ${VAR} / ${VAR:-default} from the container environment at start.
include = []
`, image)
			}
			if err := os.WriteFile(filepath.Join(dir, pack.ManifestFile), []byte(b.String()), 0o644); err != nil {
				return err
			}
			os.WriteFile(filepath.Join(dir, "plugins", ".gitkeep"), nil, 0o644)
			if !base {
				if acceptEULA {
					if err := os.WriteFile(filepath.Join(dir, "eula.txt"), []byte("# https://aka.ms/MinecraftEULA\neula=true\n"), 0o644); err != nil {
						return err
					}
				}
			}
			ensureGitignore(root)
			a.printf("created %s\n", dir)
			if !base {
				a.printf("next: serverpack add %s modrinth <project>   then   serverpack lock %s\n", args[0], args[0])
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&base, "base", false, "create a base (bases/<name>) instead of a server")
	cmd.Flags().StringSliceVar(&extends, "extends", nil, "bases to extend")
	cmd.Flags().StringVar(&src, "source", "papermc", "server jar source")
	cmd.Flags().StringVar(&project, "project", "paper", "server project (paper, folia, velocity, waterfall for papermc)")
	cmd.Flags().StringVar(&mcVersion, "mc-version", "latest", "Minecraft version (or latest)")
	cmd.Flags().StringVar(&image, "image", "eclipse-temurin:25", "base Docker image")
	cmd.Flags().BoolVar(&acceptEULA, "accept-eula", false, "write eula.txt with eula=true (you accept https://aka.ms/MinecraftEULA)")
	return cmd
}

func (a *app) addCommand() *cobra.Command {
	var (
		d      descriptor.Descriptor
		name   string
		dir    string
		noLock bool
		force  bool
	)
	cmd := &cobra.Command{
		Use:   "add <server|base:name> <source> <project|url|path>",
		Short: "Add a jar descriptor and resolve it",
		Long: `Add a jar descriptor. The third argument depends on the source:
  modrinth, hangar, curseforge, bukkitdev, spigot   project slug / id
  github                                           owner/repo
  maven                                            group:artifact (with --repo)
  jenkins, url                                     job URL / download URL
  local                                            path to a jar (copied next to the descriptor)

Examples:
  serverpack add plot251 modrinth fastasyncworldedit
  serverpack add base:common hangar ViaVersion/ViaVersion
  serverpack add plot251 github EssentialsX/Essentials --asset 'EssentialsX-*.jar'
  serverpack add plot251 jenkins https://ci.lucko.me/job/LuckPerms/ --asset 'LuckPerms-Bukkit-*.jar'
  serverpack add plot251 local ./build/libs/Internal.jar`,
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := a.root()
			if err != nil {
				return err
			}
			kind, tname, err := pack.LocateTarget(root, args[0])
			if err != nil {
				return err
			}
			d.Source = args[1]
			if _, err := source.Get(d.Source); err != nil {
				return err
			}
			ref := args[2]
			var localJar string
			switch d.Source {
			case "url", "jenkins":
				d.URL = ref
			case "local":
				localJar = ref
			default:
				d.Project = ref
			}
			if d.Source == "maven" && d.Repo == "" {
				return fmt.Errorf("maven needs --repo")
			}
			if name == "" {
				name = suggestName(d.Source, ref)
			}
			layerDir := pack.Dir(root, kind, tname)
			descPath := filepath.Join(layerDir, filepath.FromSlash(dir), name+".toml")
			if _, err := os.Stat(descPath); err == nil && !force {
				return fmt.Errorf("%s already exists (use --force to overwrite)", descPath)
			}
			if localJar != "" {
				if err := copyFile(localJar, filepath.Join(filepath.Dir(descPath), name+".jar")); err != nil {
					return err
				}
			}
			if err := descriptor.Write(descPath, &d); err != nil {
				return err
			}
			rel := path.Join(filepath.ToSlash(dir), name+".toml")
			a.printf("wrote %s\n", descPath)
			if noLock || d.Source == "local" {
				return nil
			}
			p, err := pack.LoadKind(root, kind, tname)
			if err != nil {
				return err
			}
			if p.FindEntry(rel) == nil {
				return fmt.Errorf("%s is not a jar directory of %s (jar_dirs = %v)", dir, tname, p.Manifest.JarDirs)
			}
			r := resolve.New(resolve.Options{HTTP: a.http(), Cache: a.cache(), Only: []string{rel}, Warn: a.warn})
			changes, err := r.Pack(cmd.Context(), p)
			if err != nil {
				return fmt.Errorf("%w\n(the descriptor was written; fix it and run `serverpack lock %s`)", err, args[0])
			}
			for _, c := range changes {
				a.printf("%s: %s  %s\n", c.Pack, c.Entry.Where(), describeChange(c.Old, c.New))
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&name, "name", "", "descriptor name (default: derived from the project)")
	f.StringVar(&dir, "dir", "plugins", "jar directory to add the descriptor to")
	f.StringVar(&d.Version, "version", "", `version selector: exact version, "latest" (default) or "latest-any"`)
	f.StringVar(&d.Channel, "channel", "", "least stable channel allowed: release (default), beta, alpha")
	f.StringVar(&d.Asset, "asset", "", "glob selecting the file (github, jenkins)")
	f.StringVar(&d.Repo, "repo", "", "Maven repository URL")
	f.StringVar(&d.Classifier, "classifier", "", "Maven classifier")
	f.StringVar(&d.Filename, "filename", "", "jar filename in the image (default: <name>.jar)")
	f.StringVar(&d.MCVersion, "mc-version", "", "resolve against this Minecraft version instead of the server's")
	f.StringVar(&d.Loader, "loader", "", "resolve for this platform instead of the server's")
	f.StringVar(&d.SHA256, "sha256", "", "expected sha256 (url, local)")
	f.BoolVar(&noLock, "no-lock", false, "only write the descriptor")
	f.BoolVar(&force, "force", false, "overwrite an existing descriptor")
	return cmd
}

var jenkinsJob = regexp.MustCompile(`/job/([^/]+)/?$`)

// suggestName derives a descriptor name from a source reference.
func suggestName(src, ref string) string {
	clean := func(s string) string {
		s = regexp.MustCompile(`[^A-Za-z0-9_.+-]+`).ReplaceAllString(s, "-")
		return strings.Trim(s, "-.")
	}
	switch src {
	case "local":
		return importer.DescriptorName(ref)
	case "url":
		return importer.DescriptorName(path.Base(strings.SplitN(ref, "?", 2)[0]))
	case "jenkins":
		if m := jenkinsJob.FindStringSubmatch(ref); m != nil {
			return clean(m[1])
		}
	case "maven":
		parts := strings.Split(ref, ":")
		if len(parts) >= 2 {
			return clean(parts[1])
		}
	case "spigot", "spiget":
		base := path.Base(strings.TrimSuffix(ref, "/"))
		if i := strings.LastIndex(base, "."); i > 0 {
			return clean(base[:i])
		}
	}
	return clean(path.Base(strings.TrimSuffix(ref, "/")))
}

func (a *app) removeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <server|base:name> <descriptor>",
		Short: "Remove a jar descriptor (or disable an inherited one)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := a.root()
			if err != nil {
				return err
			}
			kind, tname, err := pack.LocateTarget(root, args[0])
			if err != nil {
				return err
			}
			p, err := pack.LoadKind(root, kind, tname)
			if err != nil {
				return err
			}
			e := p.FindEntry(args[1])
			if e == nil {
				return fmt.Errorf("%s has no active descriptor named %q", tname, args[1])
			}
			if e.IsServer {
				return fmt.Errorf("the server jar can't be removed; edit the [server] table in %s instead", pack.ManifestFile)
			}
			own := filepath.Join(p.Self.Dir, filepath.FromSlash(e.File.Rel))
			if e.File.Layer == p.Self {
				if e.Desc.Source == "local" && isWithin(p.Self.Dir, e.LocalPath()) {
					if err := os.Remove(e.LocalPath()); err == nil {
						a.printf("removed %s\n", e.LocalPath())
					}
				}
				if err := os.Remove(own); err != nil {
					return err
				}
				a.printf("removed %s\n", own)
				p, err = pack.LoadKind(root, kind, tname)
				if err != nil {
					return err
				}
				if again := p.FindEntry(e.File.Rel); again == nil {
					return nil
				} else {
					a.printf("%s is still inherited from %s %s; disabling it\n", e.File.Rel, again.File.Layer.Kind, again.File.Layer.Name)
				}
			}
			if err := os.WriteFile(own, []byte("# Disables the descriptor inherited from a base.\ndisabled = true\n"), 0o644); err != nil {
				return err
			}
			a.printf("wrote %s (disabled = true)\n", own)
			return nil
		},
	}
}

// ensureGitignore makes sure the repo ignores serverpack's build staging directory.
func ensureGitignore(root string) {
	p := filepath.Join(root, ".gitignore")
	data, _ := os.ReadFile(p)
	for _, line := range strings.Split(string(data), "\n") {
		if l := strings.TrimSpace(line); l == ".serverpack/" || l == "/.serverpack/" {
			return
		}
	}
	prefix := ""
	if len(data) > 0 && !strings.HasSuffix(string(data), "\n") {
		prefix = "\n"
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(prefix + "# serverpack build staging\n/.serverpack/\n")
}

func isWithin(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)
}

func quoteList(vs []string) string {
	q := make([]string, len(vs))
	for i, v := range vs {
		q[i] = fmt.Sprintf("%q", v)
	}
	return strings.Join(q, ", ")
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
