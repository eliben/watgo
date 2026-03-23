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
