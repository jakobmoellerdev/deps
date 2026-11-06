package npm

import "testing"

func TestMastermindsRange(t *testing.T) {
	for _, tt := range []struct{ rng, anchor, v, strategy, want string }{
		{">= 1.2, < 2", "1.2.0", "2.3.0", "", ">= 2.3, < 3"},
		{">=1.2.0,<2.0.0", "1.2.0", "2.3.0", "widen", ">=1.2.0,<3.0.0"},
		{">= 1.2, < 2 || >= 3, < 4", "3.0.0", "4.1.0", "", ">= 4, < 5"},
		{"~1.2.0", "1.2.0", "1.3.4", "", "~1.3.4"},
		{"1.2.x", "1.2.0", "1.3.4", "", "1.3.x"},
		{"^1", "1.0.0", "2.0.1", "", "^2"},
		{"1.2", "1.2.0", "1.4.0", "pin", "1.4.0"},
	} {
		if a, ok := MastermindsAnchor(tt.rng); !ok || a != tt.anchor {
			t.Errorf("MastermindsAnchor(%q) = %q, %v; want %q", tt.rng, a, ok, tt.anchor)
		}
		if got, ok := NewMastermindsRange(tt.rng, tt.v, tt.strategy); !ok || got != tt.want {
			t.Errorf("NewMastermindsRange(%q, %s, %q) = %q, %v; want %q", tt.rng, tt.v, tt.strategy, got, ok, tt.want)
		}
	}
	for _, rng := range []string{"*", "1.2.3,", ", 1.2.3", "!=1.2.3", ">=1 <2 y"} {
		if a, ok := MastermindsAnchor(rng); ok {
			t.Errorf("MastermindsAnchor(%q) = %q, true", rng, a)
		}
	}
	if _, ok := NewMastermindsRange("*", "1.0.0", ""); ok {
		t.Error("NewMastermindsRange(*) rewrote")
	}
	if _, ok := NewMastermindsRange("~1.2.0", "1.2.0", ""); ok {
		t.Error("NewMastermindsRange of the anchor rewrote")
	}
	if _, ok := NewMastermindsRange("~1.2.0", "latest", ""); ok {
		t.Error("NewMastermindsRange of a non-version rewrote")
	}
	// node-semver has no commas.
	if _, ok := RangeAnchor(">= 1.2, < 2"); ok {
		t.Error("RangeAnchor accepted a comma")
	}
	if a, ok := RangeAnchor("^1.2"); !ok || a != "1.2.0" {
		t.Errorf("RangeAnchor(^1.2) = %q, %v", a, ok)
	}
	if _, ok := RangeAnchor("*"); ok {
		t.Error("RangeAnchor(*) accepted")
	}
	if r, ok := NewRange("^1.2.3", "2.0.0", ""); !ok || r != "^2.0.0" {
		t.Errorf("NewRange = %q, %v", r, ok)
	}
	if _, ok := NewRange("not a range", "2.0.0", ""); ok {
		t.Error("NewRange of an invalid range rewrote")
	}
}
