package descriptor

import (
	"strings"
	"testing"
)

const sample = `# PlotSquared from Hangar
source  = "hangar"
project = "PlotSquared"   # trailing comment
version = "latest"
`

func TestParseStrict(t *testing.T) {
	if _, err := Parse([]byte("source = \"modrinth\"\nprojct = \"x\"\n"), "x.toml"); err == nil {
		t.Fatal("expected unknown-field error")
	}
	if _, err := Parse([]byte("project = \"x\"\n"), "x.toml"); err == nil {
		t.Fatal("expected missing source error")
	}
	d, err := Parse([]byte("disabled = true\n"), "x.toml")
	if err != nil || !d.Disabled {
		t.Fatalf("disabled stub: %v %+v", err, d)
	}
}

func TestReplaceResolvedRoundTrip(t *testing.T) {
	d, err := Parse([]byte(sample), "p.toml")
	if err != nil {
		t.Fatal(err)
	}
	if !d.Stale() {
		t.Fatal("descriptor without [resolved] must be stale")
	}
	r := &Resolved{Version: "7.5.6", URL: "https://example/p.jar", SHA256: "abc", Size: 10, SpecHash: d.SpecHash()}
	out, err := ReplaceResolved([]byte(sample), ResolvedTable, r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(out), sample) {
		t.Fatalf("spec section was modified:\n%s", out)
	}
	d2, err := Parse(out, "p.toml")
	if err != nil {
		t.Fatal(err)
	}
	if d2.Stale() || d2.Resolved.Version != "7.5.6" {
		t.Fatalf("round trip failed: %+v", d2.Resolved)
	}

	// Replacing again must not duplicate the table and must keep trailing tables.
	withTrailer := string(out) + "\n[extra]\n"
	r.Version = "7.5.7"
	out2, err := ReplaceResolved([]byte(withTrailer), ResolvedTable, r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(out2), "[resolved]") != 1 || !strings.Contains(string(out2), "7.5.7") || !strings.Contains(string(out2), "[extra]") {
		t.Fatalf("bad replacement:\n%s", out2)
	}
}

func TestSpecHashChangesWithSpec(t *testing.T) {
	a, _ := Parse([]byte(sample), "a")
	b, _ := Parse([]byte(strings.Replace(sample, "latest", "7.5.6", 1)), "b")
	if a.SpecHash() == b.SpecHash() {
		t.Fatal("spec hash should change when version changes")
	}
	a.Resolved = &Resolved{Version: "x"}
	c, _ := Parse([]byte(sample), "c")
	if a.SpecHash() != c.SpecHash() {
		t.Fatal("spec hash must ignore [resolved]")
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	d := &Descriptor{Source: "url", URL: `https://x/a b"c\d.jar`, Disabled: false,
		Resolved: &Resolved{Version: "1", SHA256: "ab", Size: 42, Compat: CompatYes, SpecHash: "h"}}
	data, err := Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "'") || strings.Contains(string(data), "disabled") {
		t.Fatalf("unexpected encoding:\n%s", data)
	}
	got, err := Parse(data, "x")
	if err != nil {
		t.Fatalf("%v\n%s", err, data)
	}
	if got.URL != d.URL || *got.Resolved != *d.Resolved {
		t.Fatalf("round trip mismatch: %+v %+v", got, got.Resolved)
	}
}

func TestReplaceNestedResolved(t *testing.T) {
	manifest := `name = "x"

[server]
source = "papermc"  # paper
version = "1.21.10"

[runtime]
image = "y"
`
	r := &Resolved{Version: "1.21.10-130", SHA256: "ab", SpecHash: "h"}
	out, err := ReplaceResolved([]byte(manifest), "server.resolved", r)
	if err != nil {
		t.Fatal(err)
	}
	want := `name = "x"

[server]
source = "papermc"  # paper
version = "1.21.10"

[server.resolved]
version = "1.21.10-130"
sha256 = "ab"
spec_hash = "h"

[runtime]
image = "y"
`
	if string(out) != want {
		t.Fatalf("insert:\n%s\nwant:\n%s", out, want)
	}
	r.Version = "1.21.10-131"
	out2, err := ReplaceResolved(out, "server.resolved", r)
	if err != nil {
		t.Fatal(err)
	}
	if string(out2) != strings.Replace(want, "1.21.10-130", "1.21.10-131", 1) {
		t.Fatalf("replace:\n%s", out2)
	}
	got := string(MarshalTable("server", &Descriptor{Source: "papermc", Resolved: r}))
	if !strings.HasPrefix(got, "[server]\nsource = \"papermc\"\n\n[server.resolved]\n") {
		t.Fatalf("MarshalTable:\n%s", got)
	}
}
