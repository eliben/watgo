package tests

import "testing"

func TestParseScript_AssertMalformedBinaryModule(t *testing.T) {
	src := `(assert_malformed
  (module binary
    "\00asm" "\01\00\00\00"
    "\01\04\01\60\00\00")
  "integer too large")`

	commands, err := parseScript(src)
	if err != nil {
		t.Fatalf("parseScript failed: %v", err)
	}
	if len(commands) != 1 {
		t.Fatalf("got %d commands, want 1", len(commands))
	}
	cmd := commands[0]
	if cmd.kind != commandAssertMalformed {
		t.Fatalf("got kind %q, want %q", cmd.kind, commandAssertMalformed)
	}
	if cmd.moduleExpr == nil {
		t.Fatal("moduleExpr is nil, want parsed binary module")
	}
	if !isModuleBinaryExpr(cmd.moduleExpr) {
		t.Fatal("moduleExpr is not recognized as module binary")
	}
	if cmd.expectText != "integer too large" {
		t.Fatalf("got expectText %q, want %q", cmd.expectText, "integer too large")
	}
}

func TestParseScript_AssertReturnV128Const(t *testing.T) {
	src := `(assert_return
  (invoke "f")
  (v128.const i16x8 0 1 2 3 4 5 6 7))`

	commands, err := parseScript(src)
	if err != nil {
		t.Fatalf("parseScript failed: %v", err)
	}
	if len(commands) != 1 {
		t.Fatalf("got %d commands, want 1", len(commands))
	}
	got := commands[0].expectValues
	if len(got) != 1 {
		t.Fatalf("got %d expected values, want 1", len(got))
	}
	if got[0].kind != valueV128Const {
		t.Fatalf("got kind %q, want %q", got[0].kind, valueV128Const)
	}
	if got[0].v128Shape != "i16x8" {
		t.Fatalf("got shape %q, want i16x8", got[0].v128Shape)
	}
	want := [16]byte{0, 0, 1, 0, 2, 0, 3, 0, 4, 0, 5, 0, 6, 0, 7, 0}
	if got[0].v128 != want {
		t.Fatalf("got lanes %v, want %v", got[0].v128, want)
	}
}

func TestParseScript_InlineModuleFields(t *testing.T) {
	src := `(func) (memory 0) (func (export "f"))`

	commands, err := parseScript(src)
	if err != nil {
		t.Fatalf("parseScript failed: %v", err)
	}
	if len(commands) != 1 {
		t.Fatalf("got %d commands, want 1", len(commands))
	}
	cmd := commands[0]
	if cmd.kind != commandModule {
		t.Fatalf("got kind %q, want %q", cmd.kind, commandModule)
	}
	if got, want := cmd.quotedWAT, "(func)\n(memory 0)\n(func (export \"f\"))"; got != want {
		t.Fatalf("got quotedWAT %q, want %q", got, want)
	}
}
