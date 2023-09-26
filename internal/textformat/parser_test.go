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

	func0params := func0.TyUse.Params
	if len(func0params) != 2 {
		t.Errorf("got %d params, want 2", len(func0params))
	}
	if func0params[0].Id != "$a" || func0params[0].Ty.String() != "i32" {
		t.Errorf("got param id=%v ty=%s, want $a i32", func0params[0].Id, func0params[0].Ty)
	}
	if func0params[1].Id != "$b" || func0params[1].Ty.String() != "i32" {
		t.Errorf("got param id=%v ty=%s, want $b i32", func0params[1].Id, func0params[1].Ty)
	}

	func0result := func0.TyUse.Results[0]
	if func0result.Ty.String() != "i32" {
		t.Errorf("got result ty=%s, want i32", func0result.Ty)
	}
}
