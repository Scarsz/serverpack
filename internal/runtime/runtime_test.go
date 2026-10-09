package runtime

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Scarsz/serverpack/internal/render"
)

func env(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestExpand(t *testing.T) {
	lookup := env(map[string]string{"DB_PASS": "s3cret", "EMPTY": ""})
	cases := []struct {
		in, want string
		missing  []string
	}{
		{"password: ${DB_PASS}", "password: s3cret", nil},
		{"host: ${DB_HOST:-localhost}", "host: localhost", nil},
		{"e: ${EMPTY:-fallback}", "e: fallback", nil},
		{"e: [${EMPTY}]", "e: []", nil},
		{"literal: $${DB_PASS}", "literal: ${DB_PASS}", nil},
		{"papi: %player_name% ${ not a var } $5", "papi: %player_name% ${ not a var } $5", nil},
		{"${A} ${B} ${A}", "${A} ${B} ${A}", []string{"A", "B"}},
	}
	for _, c := range cases {
		got, missing := Expand(c.in, lookup)
		if got != c.want || !slices.Equal(missing, c.missing) {
			t.Errorf("Expand(%q) = %q %v, want %q %v", c.in, got, missing, c.want, c.missing)
		}
	}
}

func TestPrepare(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(content), 0o644)
	}
	write(render.RuntimeFile, `{"name":"s","java":"java","jvm_args":["-Xmx1G"],"jar":"server.jar","args":["--nogui"],
		"templates":["server.properties"],"volumes":["logs","plugins/X/data"]}`)
	write(render.TemplatesDir+"/server.properties", "rcon.password=${RCON}\n")
	write("server.properties", "rcon.password=old-rendered-value\n")

	if _, err := Prepare(Options{DataDir: dir, Lookup: env(nil)}); err == nil || !strings.Contains(err.Error(), "RCON (used in server.properties)") {
		t.Fatalf("expected missing var error, got %v", err)
	}
	cmd, err := Prepare(Options{DataDir: dir, Lookup: env(map[string]string{"RCON": "pw"}), ExtraArgs: []string{"--port", "25566"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(cmd, " ") != "java -Xmx1G -jar server.jar --nogui --port 25566" {
		t.Errorf("cmd = %v", cmd)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "server.properties"))
	if string(got) != "rcon.password=pw\n" {
		t.Errorf("rendered = %q", got)
	}
	if st, err := os.Stat(filepath.Join(dir, "plugins", "X", "data")); err != nil || !st.IsDir() {
		t.Errorf("volume dir not created")
	}
}
