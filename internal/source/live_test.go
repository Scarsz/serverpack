//go:build live

// Live tests hit the real distribution APIs: go test -tags live ./internal/source
package source

import (
	"context"
	"testing"

	"github.com/Scarsz/serverpack/internal/descriptor"
	"github.com/Scarsz/serverpack/internal/httpx"
)

func TestLive(t *testing.T) {
	paper := Context{MCVersion: "1.21.10", Loader: "paper"}
	cases := []struct {
		name string
		d    descriptor.Descriptor
		c    Context
	}{
		{"papermc", descriptor.Descriptor{Source: "papermc", Project: "paper", Version: "1.21.10"}, Context{}},
		{"papermc-velocity", descriptor.Descriptor{Source: "papermc", Project: "velocity"}, Context{}},
		{"purpur", descriptor.Descriptor{Source: "purpur", Version: "1.21.10"}, Context{}},
		{"fabric", descriptor.Descriptor{Source: "fabric", Version: "1.21.10"}, Context{}},
		{"modrinth", descriptor.Descriptor{Source: "modrinth", Project: "viaversion"}, paper},
		{"hangar", descriptor.Descriptor{Source: "hangar", Project: "ViaVersion/ViaVersion"}, paper},
		{"spigot", descriptor.Descriptor{Source: "spigot", Project: "luckperms.28140"}, paper},
		{"github", descriptor.Descriptor{Source: "github", Project: "EssentialsX/Essentials", Asset: "EssentialsX-*.jar"}, paper},
		{"jenkins", descriptor.Descriptor{Source: "jenkins", URL: "https://ci.lucko.me/job/LuckPerms/", Asset: "LuckPerms-Bukkit-[0-9]*.jar"}, paper},
		{"maven", descriptor.Descriptor{Source: "maven", Repo: "https://repo1.maven.org/maven2", Project: "net.luckperms:api"}, paper},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := Get(tc.d.Source)
			if err != nil {
				t.Fatal(err)
			}
			c := tc.c
			c.HTTP = httpx.New()
			r, err := s.Resolve(context.Background(), &tc.d, &c)
			if err != nil {
				t.Fatal(err)
			}
			if r.Version == "" || r.URL == "" {
				t.Fatalf("incomplete result: %+v", r)
			}
			t.Logf("%s -> %s %s (compat %s)", tc.name, r.Version, r.URL, r.Compat)
		})
	}
}
