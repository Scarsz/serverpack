package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Scarsz/serverpack/internal/render"
)

// Options configure Run.
type Options struct {
	DataDir   string
	ExtraArgs []string // appended to the server's args
	DryRun    bool     // render templates and print the command instead of exec'ing it
	Lookup    func(string) (string, bool)
	Stdout    *os.File
}

// Load reads runtime.json from dataDir.
func Load(dataDir string) (*render.Runtime, error) {
	data, err := os.ReadFile(filepath.Join(dataDir, filepath.FromSlash(render.RuntimeFile)))
	if err != nil {
		return nil, fmt.Errorf("read runtime config (is this a serverpack image?): %w", err)
	}
	var rt render.Runtime
	if err := json.Unmarshal(data, &rt); err != nil {
		return nil, fmt.Errorf("parse %s: %w", render.RuntimeFile, err)
	}
	return &rt, nil
}

// Prepare renders templates and creates volume directories. It returns the command to run.
func Prepare(opts Options) ([]string, error) {
	if opts.Lookup == nil {
		opts.Lookup = os.LookupEnv
	}
	rt, err := Load(opts.DataDir)
	if err != nil {
		return nil, err
	}

	missing := map[string][]string{}
	for _, rel := range rt.Templates {
		src := filepath.Join(opts.DataDir, filepath.FromSlash(render.TemplatesDir), filepath.FromSlash(rel))
		data, err := os.ReadFile(src)
		if err != nil {
			return nil, fmt.Errorf("template %s: %w", rel, err)
		}
		out, miss := Expand(string(data), opts.Lookup)
		for _, m := range miss {
			missing[m] = append(missing[m], rel)
		}
		dst := filepath.Join(opts.DataDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(dst, []byte(out), 0o644); err != nil {
			return nil, err
		}
	}
	if len(missing) > 0 {
		names := make([]string, 0, len(missing))
		for n := range missing {
			names = append(names, n)
		}
		sort.Strings(names)
		var b strings.Builder
		b.WriteString("missing environment variables for templated configs:")
		for _, n := range names {
			fmt.Fprintf(&b, "\n  %s (used in %s)", n, strings.Join(missing[n], ", "))
		}
		b.WriteString("\nset them (e.g. via env_file) or give a default with ${NAME:-default}")
		return nil, fmt.Errorf("%s", b.String())
	}

	for _, v := range rt.Volumes {
		p := filepath.Join(opts.DataDir, filepath.FromSlash(v))
		if _, err := os.Stat(p); err == nil {
			continue // mounted (directory or single file)
		}
		if err := os.MkdirAll(p, 0o755); err != nil {
			return nil, err
		}
	}

	cmd := rt.Command()
	return append(cmd, opts.ExtraArgs...), nil
}

// Run prepares the data dir and replaces the current process with the server.
func Run(opts Options) error {
	cmd, err := Prepare(opts)
	if err != nil {
		return err
	}
	if opts.DryRun {
		out := opts.Stdout
		if out == nil {
			out = os.Stdout
		}
		fmt.Fprintln(out, strings.Join(cmd, " "))
		return nil
	}
	bin, err := exec.LookPath(cmd[0])
	if err != nil {
		return fmt.Errorf("find %s: %w", cmd[0], err)
	}
	if err := os.Chdir(opts.DataDir); err != nil {
		return err
	}
	return execProcess(bin, cmd)
}
