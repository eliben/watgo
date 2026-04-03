package tests

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/eliben/watgo"
	"github.com/eliben/watgo/wasmir"
)

// These tests run WABT's test/interp corpus directly.
//
// For the subset handled here, each fixture is a WABT .txt file with:
//   - optional leading `;;; ...` metadata lines
//   - one embedded `(module ...)`
//   - one `(;; STDOUT ;;; ... ;;; STDOUT ;;)` block containing the expected
//     wasm-interp stdout
//
// The harness extracts the embedded module and expected stdout, compiles the
// module with watgo, runs the exported zero-argument functions under Node, and
// formats the observed results to match WABT's stdout conventions before doing
// a final string comparison.

type wabtInterpCase struct {
	moduleWAT      string
	expectedStdout string
}

type wabtInterpExport struct {
	Name       string `json:"name"`
	ResultKind string `json:"resultKind"`
}

type wabtInterpResult struct {
	Name       string `json:"name"`
	ResultKind string `json:"resultKind"`
	Value      string `json:"value"`
}

func TestWABTInterp(t *testing.T) {
	if os.Getenv("WATGO_INTEGRATION") == "0" {
		t.Skip("integration tests disabled with WATGO_INTEGRATION=0")
	}

	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Fatalf("node executable not found (set WATGO_INTEGRATION=0 to skip integration tests): %v", err)
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}

	var files []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".txt") {
			continue
		}
		files = append(files, entry.Name())
	}
	sort.Strings(files)

	if len(files) == 0 {
		t.Fatal("no .txt fixtures found")
	}

	for _, file := range files {
		t.Run(strings.TrimSuffix(file, ".txt"), func(t *testing.T) {
			runWABTInterpCase(t, nodePath, file)
		})
	}
}

// runWABTInterpCase executes one WABT interp fixture end to end.
//
// The flow is:
//   - extract the embedded module and expected stdout from the .txt fixture
//   - compile and validate the module with watgo
//   - discover which exported functions can be driven by this harness
//   - run those exports under Node
//   - compare the reconstructed stdout against WABT's expected stdout block
func runWABTInterpCase(t *testing.T, nodePath, path string) {
	t.Helper()

	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile %q failed: %v", path, err)
	}

	tc, err := extractWABTInterpCase(src)
	if err != nil {
		t.Fatalf("extractWABTInterpCase %q failed: %v", path, err)
	}

	m, err := watgo.ParseWAT([]byte(tc.moduleWAT))
	if err != nil {
		t.Fatalf("ParseWAT %q failed: %v", path, err)
	}
	if err := watgo.ValidateModule(m); err != nil {
		t.Fatalf("ValidateModule %q failed: %v", path, err)
	}

	exports, err := wabtInterpExports(m)
	if err != nil {
		t.Fatalf("wabtInterpExports %q failed: %v", path, err)
	}

	wasmBytes, err := watgo.EncodeWASM(m)
	if err != nil {
		t.Fatalf("EncodeWASM %q failed: %v", path, err)
	}

	tmpDir := t.TempDir()
	wasmPath := filepath.Join(tmpDir, strings.TrimSuffix(filepath.Base(path), ".txt")+".wasm")
	if err := os.WriteFile(wasmPath, wasmBytes, 0o644); err != nil {
		t.Fatalf("WriteFile %q failed: %v", wasmPath, err)
	}

	got, err := runWABTInterpNode(nodePath, wasmPath, exports)
	if err != nil {
		t.Fatalf("runWABTInterpNode %q failed: %v", path, err)
	}

	if got != tc.expectedStdout {
		t.Fatalf("stdout mismatch for %q:\n--- got ---\n%s\n--- want ---\n%s", path, got, tc.expectedStdout)
	}
}

// extractWABTInterpCase pulls the embedded module and expected STDOUT block out
// of one WABT run-interp fixture.
//
// This is intentionally a very small extractor for the subset we use here. It
// does not try to interpret the full WABT .txt test language; it only extracts
// the module body and the final expected stdout payload.
func extractWABTInterpCase(src []byte) (wabtInterpCase, error) {
	text := string(src)
	const stdoutStart = "(;; STDOUT ;;;"
	const stdoutEnd = ";;; STDOUT ;;)"

	moduleStart := strings.Index(text, "(module")
	if moduleStart < 0 {
		return wabtInterpCase{}, fmt.Errorf("missing module")
	}

	stdoutStartIdx := strings.Index(text, stdoutStart)
	if stdoutStartIdx < 0 {
		return wabtInterpCase{}, fmt.Errorf("missing STDOUT block")
	}
	stdoutEndIdx := strings.Index(text[stdoutStartIdx:], stdoutEnd)
	if stdoutEndIdx < 0 {
		return wabtInterpCase{}, fmt.Errorf("unterminated STDOUT block")
	}
	stdoutEndIdx += stdoutStartIdx

	moduleWAT := strings.TrimSpace(text[moduleStart:stdoutStartIdx])
	expected := text[stdoutStartIdx+len(stdoutStart) : stdoutEndIdx]
	expected = strings.TrimPrefix(expected, "\n")
	expected = strings.TrimSuffix(expected, "\n")

	return wabtInterpCase{
		moduleWAT:      moduleWAT,
		expectedStdout: expected,
	}, nil
}

// wabtInterpExports collects the exported functions that this harness will run.
//
// For now the harness models the subset used by binary.txt: exported
// zero-argument functions with zero or one scalar result. The returned
// metadata is passed to Node so it knows which exports to invoke and how to
// report their results back.
func wabtInterpExports(m *wasmir.Module) ([]wabtInterpExport, error) {
	var exports []wabtInterpExport
	for _, exp := range m.Exports {
		if exp.Kind != wasmir.ExternalKindFunction {
			continue
		}
		sig, err := wabtInterpFunctionType(m, exp.Index)
		if err != nil {
			return nil, fmt.Errorf("export %q: %w", exp.Name, err)
		}
		if len(sig.Params) != 0 {
			return nil, fmt.Errorf("export %q has %d params; only zero-arg exports are supported", exp.Name, len(sig.Params))
		}
		if len(sig.Results) > 1 {
			return nil, fmt.Errorf("export %q has %d results; only zero- or one-result exports are supported", exp.Name, len(sig.Results))
		}

		resultKind := "void"
		if len(sig.Results) == 1 {
			switch sig.Results[0].Kind {
			case wasmir.ValueKindI32:
				resultKind = "i32"
			case wasmir.ValueKindI64:
				resultKind = "i64"
			case wasmir.ValueKindF32:
				resultKind = "f32"
			case wasmir.ValueKindF64:
				resultKind = "f64"
			default:
				return nil, fmt.Errorf("export %q has unsupported result type %v", exp.Name, sig.Results[0])
			}
		}

		exports = append(exports, wabtInterpExport{Name: exp.Name, ResultKind: resultKind})
	}
	return exports, nil
}

// wabtInterpFunctionType resolves a function index through the combined import
// and defined-function index space and returns its signature from Module.Types.
func wabtInterpFunctionType(m *wasmir.Module, funcIndex uint32) (wasmir.TypeDef, error) {
	importedFuncs := uint32(0)
	for _, imp := range m.Imports {
		if imp.Kind != wasmir.ExternalKindFunction {
			continue
		}
		if importedFuncs == funcIndex {
			if int(imp.TypeIdx) >= len(m.Types) {
				return wasmir.TypeDef{}, fmt.Errorf("import function type index %d out of range", imp.TypeIdx)
			}
			return m.Types[imp.TypeIdx], nil
		}
		importedFuncs++
	}

	localIndex := funcIndex - importedFuncs
	if int(localIndex) >= len(m.Funcs) {
		return wasmir.TypeDef{}, fmt.Errorf("function index %d out of range", funcIndex)
	}
	typeIdx := m.Funcs[localIndex].TypeIdx
	if int(typeIdx) >= len(m.Types) {
		return wasmir.TypeDef{}, fmt.Errorf("function type index %d out of range", typeIdx)
	}
	return m.Types[typeIdx], nil
}

// runWABTInterpNode instantiates the compiled wasm in Node and executes the
// requested exports in order.
//
// Node reports results back as JSON rather than preformatted WABT-style text.
// For floats, the JS side returns raw IEEE-754 bit patterns so Go can format
// them with strconv.FormatFloat, which matches WABT's large-number output much
// more closely than JS number formatting does.
func runWABTInterpNode(nodePath, wasmPath string, exports []wabtInterpExport) (string, error) {
	exportsJSON, err := json.Marshal(exports)
	if err != nil {
		return "", err
	}

	script := fmt.Sprintf(`
const fs = require('node:fs');
const wasmPath = %q;
const exportsToRun = %s;

function valueString(kind, value) {
  const buf = new ArrayBuffer(8);
  const view = new DataView(buf);
  switch (kind) {
    case 'void':
      return '';
    case 'i32':
      return String(value >>> 0);
    case 'i64':
      return String(BigInt.asUintN(64, value));
    case 'f32':
      view.setFloat32(0, value, true);
      return String(view.getUint32(0, true));
    case 'f64':
      view.setFloat64(0, value, true);
      return String(view.getBigUint64(0, true));
    default:
      throw new Error('unsupported result kind: ' + kind);
  }
}

WebAssembly.instantiate(fs.readFileSync(wasmPath)).then(({instance}) => {
  const results = [];
  for (const entry of exportsToRun) {
    const fn = instance.exports[entry.name];
    const result = fn();
    results.push({
      name: entry.name,
      resultKind: entry.resultKind,
      value: valueString(entry.resultKind, result),
    });
  }
  process.stdout.write(JSON.stringify(results));
}).catch((err) => {
  console.error(err);
  process.exit(1);
});
`, wasmPath, string(exportsJSON))

	out, err := exec.Command(nodePath, "-e", script).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("node failed: %w\noutput:\n%s", err, out)
	}

	var results []wabtInterpResult
	if err := json.Unmarshal(out, &results); err != nil {
		return "", fmt.Errorf("decode node JSON: %w", err)
	}

	lines := make([]string, 0, len(results))
	for _, result := range results {
		line, err := formatWABTInterpResult(result)
		if err != nil {
			return "", err
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n"), nil
}

// formatWABTInterpResult converts one JSON result record from Node into the
// exact line format expected by WABT's interp tests.
func formatWABTInterpResult(result wabtInterpResult) (string, error) {
	if result.ResultKind == "void" {
		return result.Name + "()", nil
	}

	var formatted string
	switch result.ResultKind {
	case "i32", "i64":
		formatted = result.Value
	case "f32":
		bits, err := strconv.ParseUint(result.Value, 10, 32)
		if err != nil {
			return "", fmt.Errorf("parse f32 bits for %q: %w", result.Name, err)
		}
		formatted = strconv.FormatFloat(float64(math.Float32frombits(uint32(bits))), 'f', 6, 32)
	case "f64":
		bits, err := strconv.ParseUint(result.Value, 10, 64)
		if err != nil {
			return "", fmt.Errorf("parse f64 bits for %q: %w", result.Name, err)
		}
		formatted = strconv.FormatFloat(math.Float64frombits(bits), 'f', 6, 64)
	default:
		return "", fmt.Errorf("unsupported result kind %q", result.ResultKind)
	}
	return fmt.Sprintf("%s() => %s:%s", result.Name, result.ResultKind, formatted), nil
}
