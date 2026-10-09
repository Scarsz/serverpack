package source

import (
	"context"
	"fmt"

	"github.com/Scarsz/serverpack/internal/descriptor"
	"github.com/Scarsz/serverpack/internal/srcbuild"
)

func init() {
	Register("build", &Build{})
}

// Build describes jars built from a Gradle or Maven project. Git projects are pinned to a commit;
// local-path projects are part of the checkout and never resolved.
type Build struct{}

func (s *Build) Resolve(ctx context.Context, d *descriptor.Descriptor, c *Context) (*Result, error) {
	if err := srcbuild.ValidateSpec(d); err != nil {
		return nil, fmt.Errorf("build: %w", err)
	}
	if d.Git == "" {
		return nil, fmt.Errorf("build: path projects are not resolved")
	}
	sha, err := srcbuild.LsRemote(ctx, d.Git, d.Ref)
	if err != nil {
		return nil, fmt.Errorf("build: %w", err)
	}
	version := sha[:12]
	if d.Ref != "" && !srcbuild.IsFullSHA(d.Ref) {
		version = d.Ref + "@" + sha[:12]
	}
	return &Result{Version: version, URL: d.Git, Revision: sha, Compat: descriptor.CompatUnknown}, nil
}

// Unpinned reports whether a descriptor is part of the checkout and never locked:
// committed jars (local) and builds of local project directories.
func Unpinned(d *descriptor.Descriptor) bool {
	return d.Source == "local" || (d.Source == "build" && d.Git == "")
}
