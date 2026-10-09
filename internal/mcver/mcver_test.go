package mcver

import "testing"

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.21.10", "1.21.9", 1},
		{"1.21", "1.21.0", 0},
		{"26.3", "1.21.11", 1},
		{"1.21.11", "1.21.11-rc3", 1},
		{"1.21.11-rc3", "1.21.11-pre5", 1},
		{"26.3-rc-10", "26.3-rc-9", 1},
		{"snapshot", "1.0", -1},
	}
	for _, c := range cases {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
		if got := Compare(c.b, c.a); got != -c.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", c.b, c.a, got, -c.want)
		}
	}
}

func TestMatches(t *testing.T) {
	cases := []struct {
		target, spec string
		want         bool
	}{
		{"1.21.10", "1.21.10", true},
		{"1.21.10", "1.8-26.3", true},
		{"26.4", "1.8-26.3", false},
		{"1.21.10", "1.21.x", true},
		{"1.21", "1.21.x", true},
		{"1.22", "1.21.x", false},
		{"1.21.11-rc3", "1.21.11-rc3", true},
		{"1.21.11", "1.21.11-rc3", false},
	}
	for _, c := range cases {
		if got := Matches(c.target, c.spec); got != c.want {
			t.Errorf("Matches(%q, %q) = %v, want %v", c.target, c.spec, got, c.want)
		}
	}
}
