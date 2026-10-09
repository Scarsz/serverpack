// Package resolve implements `lock` and `update`: resolving descriptors, downloading and hashing
// the artifacts, and writing [resolved] pins back into descriptor files.
package resolve

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"

	"github.com/Scarsz/serverpack/internal/descriptor"
	"github.com/Scarsz/serverpack/internal/fetch"
	"github.com/Scarsz/serverpack/internal/httpx"
	"github.com/Scarsz/serverpack/internal/pack"
	"github.com/Scarsz/serverpack/internal/source"
)

// Options control a resolution run.
type Options struct {
	HTTP        *httpx.Client
	Cache       *fetch.Cache
	Update      bool     // re-resolve descriptors even when their pin is fresh
	Only        []string // restrict to these descriptor names or paths
	DryRun      bool     // don't download or write anything
	Concurrency int
	Warn        func(format string, args ...any)
}

// Change describes a descriptor whose pin changed (or would change, in a dry run).
type Change struct {
	Pack  string
	Entry *pack.Entry
	Old   *descriptor.Resolved
	New   *descriptor.Resolved
}

// Resolver resolves packs. Shared base descriptors are resolved at most once per Resolver.
type Resolver struct {
	opts Options
	mu   sync.Mutex
	done map[string]*descriptor.Resolved
}

// New returns a Resolver.
func New(opts Options) *Resolver {
	if opts.Concurrency <= 0 {
		opts.Concurrency = 4
	}
	if opts.Warn == nil {
		opts.Warn = func(string, ...any) {}
	}
	return &Resolver{opts: opts, done: map[string]*descriptor.Resolved{}}
}

// selected reports whether e is resolved in this run. With Only set, nothing else is touched (even stale pins).
func (r *Resolver) selected(e *pack.Entry) bool {
	if e.Desc.Source == "local" {
		return false
	}
	if len(r.opts.Only) > 0 && !r.named(e) {
		return false
	}
	return e.Desc.Stale() || r.opts.Update
}

func (r *Resolver) named(e *pack.Entry) bool {
	for _, n := range r.opts.Only {
		if n == e.Name() || n == e.File.Rel || (n == "server" && e.IsServer) {
			return true
		}
	}
	return false
}

// Pack resolves every descriptor in p that needs it, server first.
func (r *Resolver) Pack(ctx context.Context, p *pack.Pack) ([]Change, error) {
	var changes []Change
	if len(r.opts.Only) > 0 {
		for _, name := range r.opts.Only {
			if p.FindEntry(name) == nil {
				return nil, fmt.Errorf("%s: no descriptor named %q", p.Name, name)
			}
		}
	}

	base := &source.Context{HTTP: r.opts.HTTP, Warn: r.opts.Warn}
	if srv := p.Server(); srv != nil {
		if r.selected(srv) {
			ch, err := r.entry(ctx, p, srv, base)
			if err != nil {
				return nil, err
			}
			if ch != nil {
				changes = append(changes, *ch)
				if r.opts.DryRun {
					// Let plugins resolve against the would-be server version.
					d := *srv.Desc
					d.Resolved = ch.New
					srv = &pack.Entry{File: srv.File, Desc: &d, Target: srv.Target, IsServer: true}
				}
			}
		}
		base.MCVersion, base.Loader = source.ServerContext(srv.Desc)
	} else {
		r.opts.Warn("%s: no [server] table; plugins are resolved without Minecraft version filtering", p.Name)
	}

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
		sem  = make(chan struct{}, r.opts.Concurrency)
	)
	for _, e := range p.Entries {
		if e.IsServer {
			continue
		}
		c := base.ForDescriptor(e.Desc)
		if !r.selected(e) {
			if res := e.Desc.Resolved; res != nil && e.Desc.Source != "local" && res.MCVersion != c.MCVersion && c.MCVersion != "" {
				r.opts.Warn("%s: %s was resolved for Minecraft %s but the server runs %s; run `serverpack update %s %s`",
					p.Name, e.Where(), orNone(res.MCVersion), c.MCVersion, p.Self.Name, e.Name())
			}
			continue
		}
		wg.Add(1)
		go func(e *pack.Entry) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ch, err := r.entry(ctx, p, e, c)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
			} else if ch != nil {
				changes = append(changes, *ch)
			}
		}(e)
	}
	wg.Wait()
	slices.SortFunc(changes, func(a, b Change) int {
		if a.Entry.File.Rel < b.Entry.File.Rel {
			return -1
		}
		return 1
	})
	return changes, errors.Join(errs...)
}

func (r *Resolver) entry(ctx context.Context, p *pack.Pack, e *pack.Entry, c *source.Context) (*Change, error) {
	where := e.Where()
	if e.File.Layer != p.Self {
		where = fmt.Sprintf("%s (from %s %s)", e.Where(), e.File.Layer.Kind, e.File.Layer.Name)
	}

	r.mu.Lock()
	prev, seen := r.done[e.File.Abs]
	r.mu.Unlock()
	if seen {
		if prev.MCVersion != c.MCVersion && c.MCVersion != "" {
			r.opts.Warn("%s: %s is shared and was just resolved for Minecraft %s, but %s runs %s; override it in the server if it needs a different build",
				p.Name, where, orNone(prev.MCVersion), p.Name, c.MCVersion)
		}
		return nil, nil
	}

	d := e.Desc
	src, err := source.Get(d.Source)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", where, err)
	}
	if e.IsServer {
		c = &source.Context{HTTP: c.HTTP, Warn: c.Warn}
	}
	res, err := src.Resolve(ctx, d, c)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", where, err)
	}
	if !e.IsServer && d.Selector() == descriptor.Latest && res.Compat == descriptor.CompatUnknown && c.MCVersion != "" {
		r.opts.Warn("%s: %s has no Minecraft compatibility metadata; picked the newest release (%s)", p.Name, where, res.Version)
	}
	if !e.IsServer && res.Compat == descriptor.CompatNo {
		r.opts.Warn("%s: %s %s does not declare support for Minecraft %s", p.Name, where, res.Version, c.MCVersion)
	}

	n := &descriptor.Resolved{
		Version:   res.Version,
		URL:       res.URL,
		SHA256:    res.SHA256,
		Size:      res.Size,
		MCVersion: c.MCVersion,
		Loader:    c.Loader,
		Compat:    res.Compat,
		SpecHash:  d.SpecHash(),
	}
	if e.IsServer {
		n.MCVersion, n.Loader = res.MCVersion, res.Loader
		if n.Loader == "" {
			_, n.Loader = source.ServerContext(d)
		}
	}

	if !r.opts.DryRun {
		blob, err := r.opts.Cache.Fetch(ctx, res.URL, fetch.Expect{
			SHA256: res.SHA256, SHA1: res.SHA1, SHA512: res.SHA512, MD5: res.MD5, Size: res.Size, Headers: res.Headers,
		})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", where, err)
		}
		if err := checkJar(blob.Path); err != nil {
			return nil, fmt.Errorf("%s: %s: %w", where, res.URL, err)
		}
		n.SHA256, n.Size = blob.SHA256, blob.Size
	}

	r.mu.Lock()
	r.done[e.File.Abs] = n
	r.mu.Unlock()

	old := d.Resolved
	if old != nil && *old == *n {
		return nil, nil
	}
	if r.opts.DryRun && old != nil {
		// Content hashes aren't known without downloading; compare everything else.
		o := *old
		o.SHA256, o.Size = n.SHA256, n.Size
		if o == *n {
			return nil, nil
		}
	}
	if !r.opts.DryRun {
		if err := descriptor.WriteResolved(e.File.Abs, e.Table(), n); err != nil {
			return nil, err
		}
		d.Resolved = n
	}
	return &Change{Pack: p.Name, Entry: e, Old: old, New: n}, nil
}

// checkJar verifies a download is a zip/jar (catches HTML error pages and external landing pages).
func checkJar(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	head := make([]byte, 4)
	if _, err := f.Read(head); err != nil || !bytes.Equal(head, []byte("PK\x03\x04")) {
		return fmt.Errorf("download is not a jar file (an HTML page or redirect to a landing page?)")
	}
	return nil
}

func orNone(s string) string {
	if s == "" {
		return "(unknown)"
	}
	return s
}
