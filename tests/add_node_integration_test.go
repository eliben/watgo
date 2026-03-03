package tests

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eliben/watgo/internal/binaryformat"
	"github.com/eliben/watgo/internal/textformat"
	"github.com/eliben/watgo/internal/wasmir"
)

func TestAddModuleEndToEndWithNode(t *testing.T) {
	if os.Getenv("WATGO_INTEGRATION") == "0" {
		t.Skip("integration tests disabled with WATGO_INTEGRATION=0")
	}

	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Fatalf("node executable not found (set WATGO_INTEGRATION=0 to skip integration tests): %v", err)
	}

	wat := `(module
  (func (export "add") (param $a i32) (param $b i32) (result i32)
    local.get $a
    local.get $b
    i32.add
  )
)`

	ast, err := textformat.ParseModule(wat)
	if err != nil {
		t.Fatalf("ParseModule failed: %v", err)
	}

	m, lowerDiags := textformat.LowerModule(ast)
	if len(lowerDiags) > 0 {
		t.Fatalf("LowerModule diagnostics: %v", lowerDiags.Error())
	}

	validateDiags := wasmir.ValidateModule(m)
	if len(validateDiags) > 0 {
		t.Fatalf("ValidateModule diagnostics: %v", validateDiags.Error())
	}

	wasmBytes, encodeDiags := binaryformat.EncodeModule(m)
	if len(encodeDiags) > 0 {
		t.Fatalf("EncodeModule diagnostics: %v", encodeDiags.Error())
	}

	tmpDir := t.TempDir()
	wasmPath := filepath.Join(tmpDir, "add.wasm")
	if err := os.WriteFile(wasmPath, wasmBytes, 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	script := fmt.Sprintf(`
const fs = require('node:fs');
const wasm = fs.readFileSync(%q);
WebAssembly.instantiate(wasm).then(({instance}) => {
  const result = instance.exports.add(5, 7);
  console.log(String(result));
}).catch((err) => {
  console.error(err);
  process.exit(1);
});
`, wasmPath)

	out, err := exec.Command(nodePath, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("node execution failed: %v\noutput:\n%s", err, out)
	}

	got := strings.TrimSpace(string(out))
	if got != "12" {
		t.Fatalf("got node output %q, want %q", got, "12")
	}
}
