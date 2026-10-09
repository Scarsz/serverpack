// Package cli wires serverpack's commands.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"

	"github.com/spf13/cobra"

	"github.com/Scarsz/serverpack/internal/fetch"
	"github.com/Scarsz/serverpack/internal/httpx"
	"github.com/Scarsz/serverpack/internal/pack"
	"github.com/Scarsz/serverpack/internal/version"
)

// exitError carries a specific exit code without printing anything extra.
type exitError struct{ code int }

func (e exitError) Error() string { return fmt.Sprintf("exit status %d", e.code) }

type app struct {
	rootFlag  string
	cacheFlag string
	stdout    io.Writer
	stderr    io.Writer

	mu     sync.Mutex
	client *httpx.Client
}

// Execute runs the CLI and returns the process exit code.
func Execute() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	a := &app{stdout: os.Stdout, stderr: os.Stderr}
	cmd := a.rootCommand()
	if err := cmd.ExecuteContext(ctx); err != nil {
		var ee exitError
		if errors.As(err, &ee) {
			return ee.code
		}
		fmt.Fprintln(a.stderr, "error:", err)
		return 1
	}
	return 0
}

func (a *app) rootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "serverpack",
		Short:         "Git-managed Minecraft server definitions, built into Docker images",
		Version:       version.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&a.rootFlag, "root", "", "repo root (default: nearest parent with a servers/ or bases/ directory)")
	root.PersistentFlags().StringVar(&a.cacheFlag, "cache", "", "download cache directory (default: $SERVERPACK_CACHE or the user cache dir)")
	root.AddCommand(
		a.initCommand(),
		a.addCommand(),
		a.removeCommand(),
		a.importCommand(),
		a.lockCommand(),
		a.updateCommand(),
		a.renderCommand(),
		a.buildCommand(),
		a.diffCommand(),
		a.checkCommand(),
		a.runCommand(),
		a.sourcesCommand(),
	)
	return root
}

func (a *app) http() *httpx.Client {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.client == nil {
		a.client = httpx.New()
	}
	return a.client
}

func (a *app) cache() *fetch.Cache {
	dir := a.cacheFlag
	if dir == "" {
		dir = fetch.DefaultDir()
	}
	return fetch.New(dir, a.http())
}

func (a *app) root() (string, error) {
	if a.rootFlag != "" {
		return a.rootFlag, nil
	}
	return pack.FindRoot(".")
}

func (a *app) warn(format string, args ...any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	fmt.Fprintf(a.stderr, "warning: "+format+"\n", args...)
}

func (a *app) printf(format string, args ...any) {
	fmt.Fprintf(a.stdout, format, args...)
}

// targets resolves CLI names to (kind, name) pairs; no names means every server.
func (a *app) targets(root string, names []string) ([]target, error) {
	if len(names) == 0 {
		servers, err := pack.ListServers(root)
		if err != nil {
			return nil, err
		}
		if len(servers) == 0 {
			return nil, fmt.Errorf("no servers found in %s", root)
		}
		names = servers
	}
	var out []target
	for _, n := range names {
		kind, name, err := pack.LocateTarget(root, n)
		if err != nil {
			return nil, err
		}
		out = append(out, target{kind, name})
	}
	return out, nil
}

type target struct {
	kind pack.Kind
	name string
}

func (t target) load(root string) (*pack.Pack, error) {
	return pack.LoadKind(root, t.kind, t.name)
}
