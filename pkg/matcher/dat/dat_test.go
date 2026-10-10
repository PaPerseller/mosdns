package dat

import "testing"

func TestDATMatcherDomainAndFull(t *testing.T) {
	b := NewBuilder()
	b.AddRule("domain:example.com")
	b.AddRule("full:exact.example")
	m := b.Build()

	cases := []struct {
		name string
		want bool
	}{
		{"example.com", true},
		{"www.example.com", true},
		{"exact.example", true},
		{"www.exact.example", false},
		{"notexample.com", false},
		{"example.com.", true},
	}
	for _, tc := range cases {
		_, got := m.Match(tc.name)
		if got != tc.want {
			t.Errorf("Match(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestDATMatcherDuplicateRules(t *testing.T) {
	b := NewBuilder()
	b.AddRule("example.com")
	b.AddRule("example.com")
	b.AddRule("EXAMPLE.COM.")
	m := b.Build()
	if _, ok := m.Match("sub.example.com"); !ok {
		t.Fatal("duplicate normalized rules should still match")
	}
}
