package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Scarsz/serverpack/internal/build"
	"github.com/Scarsz/serverpack/internal/check"
	"github.com/Scarsz/serverpack/internal/diff"
	"github.com/Scarsz/serverpack/internal/importer"
	"github.com/Scarsz/serverpack/internal/render"
	"github.com/Scarsz/serverpack/internal/runtime"
	"github.com/Scarsz/serverpack/internal/source"
)

func (a *app) importCommand() *cobra.Command {
	opts := importer.Options{}
	cmd := &cobra.Command{
		Use:   "import <name> <live-dir>",
		Short: "Create servers/<name> from an existing server directory",
		Long: `Create servers/<name> from an existing server directory. Plugin jars are identified on Modrinth by
hash and pinned to exactly the same file; Paper/Folia server jars are matched against PaperMC builds.
Unidentified jars are vendored as source = "local". Worlds, logs, databases and other runtime state
are not imported; worlds and backups are suggested as volumes in the generated server.toml.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := a.rootOrCwd()
			if err != nil {
				return err
			}
			opts.Root, opts.Name, opts.LiveDir = root, args[0], args[1]
			opts.HTTP, opts.Cache = a.http(), a.cache()
			m, _ := source.Get("modrinth")
			pm, _ := source.Get("papermc")
			opts.Modrinth, opts.PaperMC = m.(*source.Modrinth), pm.(*source.PaperMC)
			rep, err := importer.Import(cmd.Context(), opts)
			if err != nil {
				return err
			}
			a.printf("imported into %s (%d config files)\n", rep.Dir, rep.Configs)
			if rep.Server != "" {
				a.printf("\nserver:\n  %s\n", rep.Server)
			}
			section := func(title string, items []string) {
				if len(items) == 0 {
					return
				}
				a.printf("\n%s:\n", title)
				for _, it := range items {
					a.printf("  %s\n", it)
				}
			}
			section("identified on Modrinth (pinned to the same file)", rep.Identified)
			section("vendored as local jars (switch them to a remote source where possible)", rep.Vendored)
			section("worlds (not imported; added as volumes)", rep.Worlds)
			section("skipped", rep.Skipped)
			section("volumes written to server.toml (review!)", rep.Volumes)
			a.printf("\nconfigs were copied verbatim: review them for secrets (passwords, tokens, RCON) before committing,\n")
			a.printf("and replace those with ${VAR} placeholders listed under [templates] include.\n")
			ensureGitignore(root)
			a.printf("\nnext: review %s, then `serverpack check %s` and `serverpack diff %s %s`\n",
				filepath.Join(rep.Dir, "server.toml"), opts.Name, opts.Name, opts.LiveDir)
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&opts.Force, "force", false, "import into an existing server directory")
	f.StringVar(&opts.Image, "image", "", "base image (default eclipse-temurin:25)")
	f.StringVar(&opts.MemoryMin, "xms", "", "initial heap, e.g. 128M")
	f.StringVar(&opts.MemoryMax, "xmx", "", "max heap, e.g. 24576M")
	f.StringArrayVar(&opts.JVMFlags, "jvm-flag", nil, "extra JVM flag (repeatable)")
	f.StringArrayVar(&opts.Args, "arg", nil, "server argument (repeatable)")
	f.BoolVar(&opts.Offline, "offline", false, "don't identify jars online; vendor everything")
	return cmd
}

func (a *app) renderCommand() *cobra.Command {
	var clean bool
	cmd := &cobra.Command{
		Use:   "render <server> <outdir>",
		Short: "Assemble the server's full /data tree without Docker",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := a.root()
			if err != nil {
				return err
			}
			ts, err := a.targets(root, args[:1])
			if err != nil {
				return err
			}
			p, err := ts[0].load(root)
			if err != nil {
				return err
			}
			out, err := filepath.Abs(args[1])
			if err != nil {
				return err
			}
			if entries, err := os.ReadDir(out); err == nil && len(entries) > 0 {
				if !clean {
					return fmt.Errorf("%s is not empty (use --clean to replace it)", out)
				}
				if isWithin(out, root) {
					return fmt.Errorf("refusing to clean %s: it contains the repo", out)
				}
				if err := os.RemoveAll(out); err != nil {
					return err
				}
			}
			rt, err := render.Render(cmd.Context(), p, render.Options{Cache: a.cache(), JarsDir: out, ConfigDir: out, Warn: a.warn})
			if err != nil {
				return err
			}
			a.printf("rendered %s to %s\ncommand: %s\n", p.Name, out, strings.Join(rt.Command(), " "))
			if len(rt.Templates) > 0 {
				a.printf("templates (rendered by `serverpack run --data %s`): %s\n", out, strings.Join(rt.Templates, ", "))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&clean, "clean", false, "delete outdir first if it is not empty")
	return cmd
}

func (a *app) buildCommand() *cobra.Command {
	var opts build.Options
	cmd := &cobra.Command{
		Use:   "build <server...>",
		Short: "Build Docker images for servers",
		Long: `Render each server into a build context (.serverpack/build/<server>/) and run docker buildx build,
tagging every [build] images x tags combination. Placeholders: {name}, {mc_version}, {git_sha}, {date}.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := a.root()
			if err != nil {
				return err
			}
			ts, err := a.targets(root, args)
			if err != nil {
				return err
			}
			if !cmd.Flags().Changed("image") {
				opts.Images = nil
			}
			if !cmd.Flags().Changed("tag") {
				opts.Tags = nil
			}
			if !cmd.Flags().Changed("platform") {
				opts.Platforms = nil
			}
			opts.Cache, opts.Warn, opts.Stdout, opts.Stderr = a.cache(), a.warn, a.stdout, a.stderr
			for _, t := range ts {
				p, err := t.load(root)
				if err != nil {
					return err
				}
				res, err := build.Build(cmd.Context(), p, opts)
				if err != nil {
					return fmt.Errorf("%s: %w", p.Name, err)
				}
				if opts.DryRun {
					a.printf("%s: context staged at %s\n%s\n", p.Name, res.ContextDir, strings.Join(res.Command, " "))
					continue
				}
				verb := "built"
				if opts.Push {
					verb = "pushed"
				}
				a.printf("%s: %s %s\n", p.Name, verb, strings.Join(res.Refs, ", "))
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringArrayVar(&opts.Images, "image", nil, "image name (repeatable; replaces [build] images)")
	f.StringArrayVar(&opts.Tags, "tag", nil, "tag (repeatable; replaces [build] tags)")
	f.StringArrayVar(&opts.Platforms, "platform", nil, "target platform, e.g. linux/amd64 (repeatable)")
	f.BoolVar(&opts.Push, "push", false, "push instead of loading into the local docker daemon")
	f.StringVar(&opts.EntrypointBin, "entrypoint-bin", "", "copy this linux serverpack binary into the image instead of using the published one")
	f.StringVar(&opts.ServerpackImage, "serverpack-image", "", "image providing /serverpack (default "+build.DefaultServerpackImage()+")")
	f.BoolVar(&opts.DryRun, "dry-run", false, "stage the build context and print the docker command without running it")
	f.StringVar(&opts.Docker, "docker", "docker", "docker CLI to invoke")
	return cmd
}

func (a *app) diffCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "diff <server> <live-dir>",
		Short: "Show drift between a server definition and a live server directory",
		Long: `Compare what the image would contain with a live server directory.
  +  only in the pack     -  only on the live server     ~  differs     ?  can't compare
Volume paths, worlds and generated runtime files are ignored; jars are compared by content.
Templated files are compared after expanding them with the current environment.
Exits with status 1 when there are differences.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := a.root()
			if err != nil {
				return err
			}
			ts, err := a.targets(root, args[:1])
			if err != nil {
				return err
			}
			p, err := ts[0].load(root)
			if err != nil {
				return err
			}
			entries, err := diff.Diff(cmd.Context(), p, args[1], diff.Options{Cache: a.cache()})
			if err != nil {
				return err
			}
			if len(entries) == 0 {
				a.printf("no differences\n")
				return nil
			}
			for _, e := range entries {
				a.printf("%s\n", e)
			}
			return exitError{1}
		},
	}
}

func (a *app) checkCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "check [server|base:name...]",
		Short: "Validate servers offline (all servers by default)",
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := a.root()
			if err != nil {
				return err
			}
			ts, err := a.targets(root, args)
			if err != nil {
				return err
			}
			errorsFound, warnings := 0, 0
			for _, t := range ts {
				p, err := t.load(root)
				if err != nil {
					fmt.Fprintf(a.stderr, "error: %s\n", err)
					errorsFound++
					continue
				}
				for _, f := range check.Pack(p) {
					fmt.Fprintf(a.stderr, "%s: %s\n", f.Level, f.Msg)
					if f.Level == check.Error {
						errorsFound++
					} else {
						warnings++
					}
				}
			}
			a.printf("checked %d target(s): %d error(s), %d warning(s)\n", len(ts), errorsFound, warnings)
			if errorsFound > 0 {
				return exitError{1}
			}
			return nil
		},
	}
}

func (a *app) runCommand() *cobra.Command {
	var opts runtime.Options
	cmd := &cobra.Command{
		Use:   "run [-- extra server args]",
		Short: "Container entrypoint: render templates, prepare volumes and exec java",
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.ExtraArgs = args
			return runtime.Run(opts)
		},
	}
	cmd.Flags().StringVar(&opts.DataDir, "data", ".", "server data directory")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "render templates and print the command instead of running it")
	return cmd
}

func (a *app) sourcesCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "sources",
		Short: "List supported jar sources",
		RunE: func(cmd *cobra.Command, args []string) error {
			help := map[string]string{
				"papermc":    `server jars from PaperMC Fill: project = paper|folia|velocity|waterfall, version = MC version|latest, build = N|latest`,
				"purpur":     `Purpur server jars: version = MC version|latest, build = N|latest`,
				"fabric":     `Fabric server launcher: version = MC version|latest, loader_version, installer_version`,
				"modrinth":   `project = slug or id; filters by MC version + loader`,
				"hangar":     `project = slug or Owner/Slug; filters by platform + MC version`,
				"spigot":     `project = resource id (or name.id) via Spiget; no premium/external resources`,
				"spiget":     `alias of spigot`,
				"curseforge": `project = slug or id; needs CURSEFORGE_API_KEY`,
				"bukkitdev":  `BukkitDev via CurseForge; project = slug or id; needs CURSEFORGE_API_KEY`,
				"github":     `project = owner/repo, asset = glob, version = tag|latest; GITHUB_TOKEN optional`,
				"jenkins":    `url = job URL, asset = glob, version = build number|latest`,
				"maven":      `repo = URL, project = group:artifact[:classifier], version = exact|latest`,
				"url":        `url = direct download; sha256 optional (pinned at lock)`,
				"local":      `jar committed next to the descriptor (path = override)`,
			}
			for _, n := range source.Names() {
				a.printf("%-11s %s\n", n, help[n])
			}
			return nil
		},
	}
}
