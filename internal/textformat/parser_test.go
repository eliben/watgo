package textformat

import (
	"testing"
)

func TestParseSmoke(t *testing.T) {
	// Smoke test for parsing a module, checking the parsed AST without using
	// its textual/debug representation.
	wat := `
	(module $mod
		(func $add (export "add") (param $a i32) (param $b i32) (result i32)))`
	m, err := ParseModule(wat)
	if err != nil {
		t.Fatal(err)
	}

	if m.Name != "$mod" {
		t.Errorf("got mod name %v, want $mod", m.Name)
	}
	if m.loc.String() != "2:2" {
		t.Errorf("got mod loc %s, want 2:2", m.loc)
	}

}
