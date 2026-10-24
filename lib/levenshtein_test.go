package lib

import "testing"

func TestLevenshtein(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"", "abc", 3},
		{"abc", "", 3},
		{"abc", "abc", 0},
		{"kitten", "sitting", 3},
		{"team:data-eg", "team:data-eng", 1},
		{"bash", "exec:bash", 5},
		{"flaw", "lawn", 2},
		{"gumbo", "gambol", 2},
		{"saturday", "sunday", 3},
	}
	for _, c := range cases {
		got := Levenshtein(c.a, c.b)
		if got != c.want {
			t.Errorf("Levenshtein(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestLevenshtein_Symmetric(t *testing.T) {
	pairs := [][2]string{
		{"kitten", "sitting"},
		{"team:data-eg", "team:data-eng"},
		{"", "nonempty"},
		{"same", "same"},
	}
	for _, p := range pairs {
		got := Levenshtein(p[0], p[1])
		rev := Levenshtein(p[1], p[0])
		if got != rev {
			t.Errorf("Levenshtein(%q, %q) = %d but Levenshtein(%q, %q) = %d; expected symmetric",
				p[0], p[1], got, p[1], p[0], rev)
		}
	}
}

func TestClosestMatches(t *testing.T) {
	candidates := []string{"overview", "authoring", "schema", "tags", "usage", "api"}

	got := ClosestMatches("authring", candidates, 3)
	if len(got) != 3 || got[0] != "authoring" {
		t.Errorf("ClosestMatches(authring) = %v, want authoring first of 3", got)
	}

	// Case-insensitive, and ties break alphabetically for a stable message.
	got = ClosestMatches("TAGS", []string{"zags", "tags", "bags"}, 3)
	want := []string{"tags", "bags", "zags"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ClosestMatches(TAGS) = %v, want %v", got, want)
		}
	}

	if got := ClosestMatches("x", []string{"a"}, 3); len(got) != 1 {
		t.Errorf("fewer candidates than n should return them all, got %v", got)
	}
	if got := ClosestMatches("x", nil, 3); len(got) != 0 {
		t.Errorf("no candidates should return none, got %v", got)
	}
}
