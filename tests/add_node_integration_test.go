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

	m, lowerErr := textformat.LowerModule(ast)
	if lowerErr != nil {
		t.Fatalf("LowerModule error: %v", lowerErr)
	}

	validateErr := wasmir.ValidateModule(m)
	if validateErr != nil {
		t.Fatalf("ValidateModule error: %v", validateErr)
	}

	wasmBytes, encodeErr := binaryformat.EncodeModule(m)
	if encodeErr != nil {
		t.Fatalf("EncodeModule error: %v", encodeErr)
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
