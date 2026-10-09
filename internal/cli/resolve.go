package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Scarsz/serverpack/internal/descriptor"
	"github.com/Scarsz/serverpack/internal/resolve"
)

func (a *app) lockCommand() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "lock [server...]",
		Short: "Resolve descriptors that are unpinned or whose spec changed (all servers by default)",
		Long: `Resolve every descriptor without a [resolved] pin, or whose spec changed since it was pinned.
Existing fresh pins are left alone; use "update" to move floating versions forward.
Names may be servers, or bases as base:<name>.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.resolve(cmd, args, nil, false, dryRun)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would change without downloading or writing")
	return cmd
}

func (a *app) updateCommand() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "update [server] [descriptor...]",
		Short: "Re-resolve descriptors to the newest matching versions",
		Long: `Re-resolve descriptors: "latest" picks the newest release that supports the server's Minecraft
version, "latest-any" the newest release, exact pins stay put but get their compatibility re-checked.
With no server, every server is updated. Descriptor names are file names without .toml (e.g. ViaVersion),
or "server" for the [server] table of server.toml.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			var servers, only []string
			if len(args) > 0 {
				servers, only = args[:1], args[1:]
			}
			return a.resolve(cmd, servers, only, true, dryRun)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would change without downloading or writing")
	return cmd
}

func (a *app) resolve(cmd *cobra.Command, names, only []string, update, dryRun bool) error {
	root, err := a.root()
	if err != nil {
		return err
	}
	targets, err := a.targets(root, names)
	if err != nil {
		return err
	}
	r := resolve.New(resolve.Options{
		HTTP: a.http(), Cache: a.cache(), Update: update, Only: only, DryRun: dryRun, Warn: a.warn,
	})
	var errs []error
	total := 0
	for _, t := range targets {
		// Load each pack right before resolving it so shared base descriptors written by an earlier pack are re-read.
		p, err := t.load(root)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		changes, err := r.Pack(cmd.Context(), p)
		for _, c := range changes {
			total++
			a.printf("%s: %s  %s\n", c.Pack, c.Entry.Where(), describeChange(c.Old, c.New))
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	switch {
	case total == 0:
		a.printf("everything is up to date\n")
	case dryRun:
		a.printf("%d change(s) (dry run, nothing written)\n", total)
	}
	return nil
}

func describeChange(old, n *descriptor.Resolved) string {
	compat := ""
	if n.Compat == descriptor.CompatNo {
		compat = fmt.Sprintf(" [does not declare support for %s]", n.MCVersion)
	}
	switch {
	case old == nil:
		return "pinned " + n.Version + compat
	case old.Version != n.Version:
		return old.Version + " -> " + n.Version + compat
	default:
		var what []string
		if old.SHA256 != n.SHA256 {
			what = append(what, "content")
		}
		if old.MCVersion != n.MCVersion {
			what = append(what, "mc "+orDash(old.MCVersion)+" -> "+orDash(n.MCVersion))
		}
		if old.SpecHash != n.SpecHash {
			what = append(what, "spec")
		}
		if len(what) == 0 {
			what = append(what, "metadata")
		}
		return n.Version + " (" + strings.Join(what, ", ") + " updated)" + compat
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
