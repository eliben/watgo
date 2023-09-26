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

	if m.Id != "$mod" {
		t.Errorf("got mod id %v, want $mod", m.Id)
	}
	if m.loc.String() != "2:2" {
		t.Errorf("got mod loc %s, want 2:2", m.loc)
	}

	func0 := m.Funcs[0]
	if func0.Id != "$add" {
		t.Errorf("got func id %v, want $add", func0.Id)
	}
	if func0.Export != "add" {
		t.Errorf("got func export %v, want add", func0.Export)
	}
}
