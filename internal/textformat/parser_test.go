package textformat

import (
	"testing"
)

func TestParseSmoke(t *testing.T) {
	// Smoke test for parsing a module, checking the parsed AST without using
	// its textual/debug representation.
	wat := `
	(module $mod
		(func $add (export "add") (param $a i32) (param $b i32) (result i32)
			(local $i i64)
			(local f32)
		)
	)`
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
	param0, param1 := func0params[0], func0params[1]
	if param0.Id != "$a" || param0.Ty.String() != "i32" {
		t.Errorf("got param id=%v ty=%s, want $a i32", param0.Id, param0.Ty)
	}
	if param1.Id != "$b" || param1.Ty.String() != "i32" {
		t.Errorf("got param id=%v ty=%s, want $b i32", param1.Id, param1.Ty)
	}

	result0 := func0.TyUse.Results[0]
	if result0.Ty.String() != "i32" {
		t.Errorf("got result ty=%s, want i32", result0.Ty)
	}

	if len(func0.Locals) != 2 {
		t.Errorf("got %d locals, want 2", len(func0.Locals))
	}
	local0 := func0.Locals[0]
	if local0.Id != "$i" || local0.Ty.String() != "i64" {
		t.Errorf("got param id=%v ty=%s, want $i i64", local0.Id, local0.Ty)
	}
	local1 := func0.Locals[1]
	if local1.Id != "" || local1.Ty.String() != "f32" {
		t.Errorf("got param id=%v ty=%s, want <empty> f32", local1.Id, local1.Ty)
	}
}
