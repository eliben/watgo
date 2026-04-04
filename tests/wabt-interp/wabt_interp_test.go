package tests

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
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
	expectedStderr string
	expectedError  int
	runArgs        []string
}

type wabtInterpExport struct {
	Name       string   `json:"name"`
	Kind       string   `json:"kind"`
	ResultKind string   `json:"resultKind"`
	ParamKinds []string `json:"paramKinds"`
}

type wabtInterpResult struct {
	Name       string `json:"name"`
	ResultKind string `json:"resultKind"`
	ArgText    string `json:"argText"`
	Value      string `json:"value"`
	Error      string `json:"error"`
	// StdoutCount records how many auxiliary stdout lines JS had produced before
	// this invocation finished, so Go can interleave host-call logging with the
	// final WABT-style result line in the original execution order.
	StdoutCount int `json:"stdoutCount"`
}

type wabtInterpRunResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

type wabtInterpInvocation struct {
	ExportName string                    `json:"exportName"`
	Args       []wabtInterpInvocationArg `json:"args"`
}

type wabtInterpInvocationArg struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

type wabtInterpImport struct {
	Module     string   `json:"module"`
	Name       string   `json:"name"`
	ResultKind string   `json:"resultKind"`
	ParamKinds []string `json:"paramKinds"`
}

type wabtInterpNodePayload struct {
	WasmPath            string                 `json:"wasmPath"`
	Exports             []wabtInterpExport     `json:"exports"`
	Imports             []wabtInterpImport     `json:"imports"`
	Invocations         []wabtInterpInvocation `json:"invocations"`
	V128ResultHelperB64 string                 `json:"v128ResultHelperB64"`
	HostPrint           bool                   `json:"hostPrint"`
	DummyImportFunc     bool                   `json:"dummyImportFunc"`
	HostPrintResultKind string                 `json:"hostPrintResultKind"`
}

// wabtInterpSkippedFixtures lists WABT fixtures we keep in-tree but do not run
// with this Node-backed harness.
var wabtInterpSkippedFixtures = []string{
	// basic-logging expects wabt's wat2wasm/wasm-interp verbose stderr, with
	// section-size fixups and decoder callback logs. This harness only runs the
	// compiled module under Node; it does not emulate wabt's tool logging.
	"basic-logging.txt",

	// basic-tracing expects wabt interpreter trace output including wabt-specific
	// instruction PCs, stack-depth annotations, and lowered control-flow ops
	// like br_unless/drop_keep. That is effectively an interpreter trace mode,
	// not a small execution-harness variation.
	"basic-tracing.txt",

	// The exception fixtures below use WABT's older structured EH syntax with
	// try/do/catch/rethrow/delegate. watgo currently supports the newer
	// try_table-based exception subset in wasmspec, but not this text form.
	"rethrow.txt",
	"rethrow-and-br.txt",
	"throw-across-frame.txt",
	"try.txt",
	"try-delegate.txt",

	// custom-page-sizes is a run-interp-spec fixture for the custom-page-sizes
	// proposal. This Node-backed harness only supports the simpler run-interp
	// subset, and watgo does not implement custom page sizes in its main
	// text/binary pipeline.
	"custom-page-sizes.txt",
}

func wabtInterpShouldSkipFixture(name string) bool {
	return slices.Contains(wabtInterpSkippedFixtures, name)
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
			if wabtInterpShouldSkipFixture(file) {
				t.Skip("fixture is intentionally not covered by this harness")
			}
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

	hostPrintResultKind, err := wabtInterpHostPrintResultKind(m)
	if err != nil {
		t.Fatalf("wabtInterpHostPrintResultKind %q failed: %v", path, err)
	}

	imports, err := wabtInterpImports(m)
	if err != nil {
		t.Fatalf("wabtInterpImports %q failed: %v", path, err)
	}

	got, err := runWABTInterpNode(nodePath, wasmPath, exports, imports, tc.runArgs, hostPrintResultKind)
	if err != nil {
		t.Fatalf("runWABTInterpNode %q failed: %v", path, err)
	}

	if got.ExitCode != tc.expectedError {
		t.Fatalf("exit code mismatch for %q: got %d, want %d\nstdout:\n%s\nstderr:\n%s", path, got.ExitCode, tc.expectedError, got.Stdout, got.Stderr)
	}
	if !wabtInterpStdoutMatches(got.Stdout, tc.expectedStdout) {
		t.Fatalf("stdout mismatch for %q:\n--- got ---\n%s\n--- want ---\n%s", path, got.Stdout, tc.expectedStdout)
	}
	if got.Stderr != tc.expectedStderr {
		t.Fatalf("stderr mismatch for %q:\n--- got ---\n%s\n--- want ---\n%s", path, got.Stderr, tc.expectedStderr)
	}
}

// extractWABTInterpCase pulls the embedded module and expected STDOUT block out
// of one WABT run-interp fixture.
//
// This is intentionally a very small extractor for the subset we use here. It
// does not try to interpret the full WABT .txt test language; it only extracts
// the module body plus any final expected stdout/stderr payloads. Some
// run-interp fixtures are still useful even without an explicit output block;
// for those, the expected stdout/stderr simply stay empty.
func extractWABTInterpCase(src []byte) (wabtInterpCase, error) {
	text := string(src)
	const stdoutStart = "(;; STDOUT ;;;"
	const stdoutEnd = ";;; STDOUT ;;)"
	const stderrStart = "(;; STDERR ;;;"
	const stderrEnd = ";;; STDERR ;;)"

	moduleStart := strings.Index(text, "(module")
	if moduleStart < 0 {
		return wabtInterpCase{}, fmt.Errorf("missing module")
	}

	metaLines := strings.Split(text[:moduleStart], "\n")
	var runArgs []string
	expectedError := 0
	for _, line := range metaLines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, ";;; ") {
			continue
		}
		body := strings.TrimPrefix(line, ";;; ")
		key, value, ok := strings.Cut(body, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch key {
		case "ARGS", "ARGS1", "ARGS*":
			runArgs = append(runArgs, strings.Fields(value)...)
		case "ERROR":
			n, err := strconv.Atoi(value)
			if err != nil {
				return wabtInterpCase{}, fmt.Errorf("invalid ERROR value %q", value)
			}
			expectedError = n
		}
	}

	endIdx := len(text)
	expectedStdout := ""
	if stdoutStartIdx := strings.Index(text, stdoutStart); stdoutStartIdx >= 0 {
		stdoutEndIdx := strings.Index(text[stdoutStartIdx:], stdoutEnd)
		if stdoutEndIdx < 0 {
			return wabtInterpCase{}, fmt.Errorf("unterminated STDOUT block")
		}
		stdoutEndIdx += stdoutStartIdx
		endIdx = min(endIdx, stdoutStartIdx)
		expectedStdout = text[stdoutStartIdx+len(stdoutStart) : stdoutEndIdx]
		expectedStdout = strings.TrimPrefix(expectedStdout, "\n")
		expectedStdout = strings.TrimSuffix(expectedStdout, "\n")
	}

	expectedStderr := ""
	if stderrStartIdx := strings.Index(text, stderrStart); stderrStartIdx >= 0 {
		stderrEndIdx := strings.Index(text[stderrStartIdx:], stderrEnd)
		if stderrEndIdx < 0 {
			return wabtInterpCase{}, fmt.Errorf("unterminated STDERR block")
		}
		stderrEndIdx += stderrStartIdx
		endIdx = min(endIdx, stderrStartIdx)
		expectedStderr = text[stderrStartIdx+len(stderrStart) : stderrEndIdx]
		expectedStderr = strings.TrimPrefix(expectedStderr, "\n")
		expectedStderr = strings.TrimSuffix(expectedStderr, "\n")
	}

	moduleWAT := strings.TrimSpace(text[moduleStart:endIdx])

	return wabtInterpCase{
		moduleWAT:      moduleWAT,
		expectedStdout: expectedStdout,
		expectedStderr: expectedStderr,
		expectedError:  expectedError,
		runArgs:        runArgs,
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
		exportInfo := wabtInterpExport{Name: exp.Name}
		if exp.Kind != wasmir.ExternalKindFunction {
			exportInfo.Kind = "nonfunc"
			exports = append(exports, exportInfo)
			continue
		}
		exportInfo.Kind = "func"
		sig, err := wabtInterpFunctionType(m, exp.Index)
		if err != nil {
			return nil, fmt.Errorf("export %q: %w", exp.Name, err)
		}
		if len(sig.Results) > 1 {
			return nil, fmt.Errorf("export %q has %d results; only zero- or one-result exports are supported", exp.Name, len(sig.Results))
		}
		for _, param := range sig.Params {
			kind, err := wabtInterpValueKind(param)
			if err != nil {
				return nil, fmt.Errorf("export %q has unsupported param type %v", exp.Name, param)
			}
			exportInfo.ParamKinds = append(exportInfo.ParamKinds, kind)
		}

		resultKind := "void"
		if len(sig.Results) == 1 {
			var err error
			resultKind, err = wabtInterpValueKind(sig.Results[0])
			if err != nil {
				return nil, fmt.Errorf("export %q has unsupported result type %v", exp.Name, sig.Results[0])
			}
		}

		exportInfo.ResultKind = resultKind
		exports = append(exports, exportInfo)
	}
	return exports, nil
}

func wabtInterpValueKind(vt wasmir.ValueType) (string, error) {
	switch vt.Kind {
	case wasmir.ValueKindI32:
		return "i32", nil
	case wasmir.ValueKindI64:
		return "i64", nil
	case wasmir.ValueKindF32:
		return "f32", nil
	case wasmir.ValueKindF64:
		return "f64", nil
	case wasmir.ValueKindV128:
		return "v128", nil
	case wasmir.ValueKindRef:
		switch vt.HeapType.Kind {
		case wasmir.HeapKindFunc, wasmir.HeapKindNoFunc:
			return "funcref", nil
		case wasmir.HeapKindExtern, wasmir.HeapKindNoExtern:
			return "externref", nil
		}
	}
	return "", fmt.Errorf("unsupported value type %v", vt)
}

func wabtInterpHostPrintResultKind(m *wasmir.Module) (string, error) {
	resultKind := ""
	for _, imp := range m.Imports {
		if imp.Kind != wasmir.ExternalKindFunction || imp.Module != "host" || imp.Name != "print" {
			continue
		}
		if int(imp.TypeIdx) >= len(m.Types) {
			return "", fmt.Errorf("import function type index %d out of range", imp.TypeIdx)
		}
		sig := m.Types[imp.TypeIdx]
		kind := "void"
		if len(sig.Results) > 1 {
			return "", fmt.Errorf("host.print import has %d results", len(sig.Results))
		}
		if len(sig.Results) == 1 {
			var err error
			kind, err = wabtInterpValueKind(sig.Results[0])
			if err != nil {
				return "", fmt.Errorf("unsupported host.print result type %v", sig.Results[0])
			}
		}
		if resultKind == "" {
			resultKind = kind
			continue
		}
		if resultKind != kind {
			return "", fmt.Errorf("mixed host.print result kinds %q and %q", resultKind, kind)
		}
	}
	return resultKind, nil
}

// wabtInterpImports extracts imported function signatures for the small
// run-interp flags that synthesize host functions in the harness, such as
// `--dummy-import-func`.
func wabtInterpImports(m *wasmir.Module) ([]wabtInterpImport, error) {
	var imports []wabtInterpImport
	for _, imp := range m.Imports {
		if imp.Kind != wasmir.ExternalKindFunction {
			continue
		}
		if int(imp.TypeIdx) >= len(m.Types) {
			return nil, fmt.Errorf("import function type index %d out of range", imp.TypeIdx)
		}
		sig := m.Types[imp.TypeIdx]
		if len(sig.Results) > 1 {
			return nil, fmt.Errorf("import %q.%q has %d results", imp.Module, imp.Name, len(sig.Results))
		}

		importInfo := wabtInterpImport{
			Module:     imp.Module,
			Name:       imp.Name,
			ResultKind: "void",
		}
		for _, param := range sig.Params {
			kind, err := wabtInterpValueKind(param)
			if err != nil {
				return nil, fmt.Errorf("import %q.%q has unsupported param type %v", imp.Module, imp.Name, param)
			}
			importInfo.ParamKinds = append(importInfo.ParamKinds, kind)
		}
		if len(sig.Results) == 1 {
			kind, err := wabtInterpValueKind(sig.Results[0])
			if err != nil {
				return nil, fmt.Errorf("import %q.%q has unsupported result type %v", imp.Module, imp.Name, sig.Results[0])
			}
			importInfo.ResultKind = kind
		}
		imports = append(imports, importInfo)
	}
	return imports, nil
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
func runWABTInterpNode(nodePath, wasmPath string, exports []wabtInterpExport, imports []wabtInterpImport, runArgs []string, hostPrintResultKind string) (wabtInterpRunResult, error) {
	invocations, hostPrint, dummyImportFunc, err := wabtInterpInvocations(exports, runArgs)
	if err != nil {
		return wabtInterpRunResult{}, err
	}
	if runResult, ok := wabtInterpValidateInvocations(exports, invocations); ok {
		return runResult, nil
	}
	helperB64, err := wabtInterpV128ResultHelperBase64(exports)
	if err != nil {
		return wabtInterpRunResult{}, err
	}
	return runWABTInterpNodeWithInvocations(nodePath, wasmPath, exports, imports, invocations, helperB64, hostPrint, dummyImportFunc, hostPrintResultKind)
}

func wabtInterpInvocations(exports []wabtInterpExport, runArgs []string) ([]wabtInterpInvocation, bool, bool, error) {
	exportMap := make(map[string]wabtInterpExport, len(exports))
	for _, exp := range exports {
		exportMap[exp.Name] = exp
	}

	hostPrint := false
	dummyImportFunc := false
	var invocations []wabtInterpInvocation
	current := -1
	for _, arg := range runArgs {
		switch {
		case arg == "--host-print":
			hostPrint = true
		case arg == "--dummy-import-func":
			dummyImportFunc = true
		case strings.HasPrefix(arg, "--enable-"):
			// These WABT flags enable proposal features in wabt's own tools. The
			// Node runtime used here either supports the needed feature already or
			// will fail later for a real compiler/runtime gap.
		case strings.HasPrefix(arg, "--run-export="):
			name := strings.TrimPrefix(arg, "--run-export=")
			invocations = append(invocations, wabtInterpInvocation{ExportName: name, Args: []wabtInterpInvocationArg{}})
			current = len(invocations) - 1
		case strings.HasPrefix(arg, "--argument="):
			if current < 0 {
				return nil, false, false, fmt.Errorf("--argument without preceding --run-export")
			}
			spec := strings.TrimPrefix(arg, "--argument=")
			kind, text, ok := strings.Cut(spec, ":")
			if !ok {
				return nil, false, false, fmt.Errorf("invalid --argument value %q", spec)
			}
			invocations[current].Args = append(invocations[current].Args, wabtInterpInvocationArg{Kind: kind, Text: text})
		default:
			return nil, false, false, fmt.Errorf("unsupported wabt interp arg %q", arg)
		}
	}

	if len(invocations) == 0 {
		for _, exp := range exports {
			if exp.Kind == "func" && len(exp.ParamKinds) == 0 {
				invocations = append(invocations, wabtInterpInvocation{ExportName: exp.Name, Args: []wabtInterpInvocationArg{}})
			}
		}
	}

	for _, inv := range invocations {
		if _, ok := exportMap[inv.ExportName]; !ok {
			return nil, false, false, fmt.Errorf("unknown export %q", inv.ExportName)
		}
	}
	return invocations, hostPrint, dummyImportFunc, nil
}

// wabtInterpValidateInvocations handles the run-interp command-shape errors
// that WABT reports before module execution, for example a `--run-export`
// invocation with the wrong number of `--argument=` values.
func wabtInterpValidateInvocations(exports []wabtInterpExport, invocations []wabtInterpInvocation) (wabtInterpRunResult, bool) {
	exportMap := make(map[string]wabtInterpExport, len(exports))
	for _, exp := range exports {
		exportMap[exp.Name] = exp
	}
	for _, inv := range invocations {
		exp, ok := exportMap[inv.ExportName]
		if !ok || exp.Kind != "func" {
			continue
		}
		if len(inv.Args) == len(exp.ParamKinds) {
			continue
		}
		return wabtInterpRunResult{
			Stdout:   fmt.Sprintf("Exported function '%s' expects %d arguments, but %d were provided", inv.ExportName, len(exp.ParamKinds), len(inv.Args)),
			ExitCode: 1,
		}, true
	}
	return wabtInterpRunResult{}, false
}

func runWABTInterpNodeWithInvocations(nodePath, wasmPath string, exports []wabtInterpExport, imports []wabtInterpImport, invocations []wabtInterpInvocation, helperB64 string, hostPrint bool, dummyImportFunc bool, hostPrintResultKind string) (wabtInterpRunResult, error) {
	payloadBytes, err := json.Marshal(wabtInterpNodePayload{
		WasmPath:            wasmPath,
		Exports:             exports,
		Imports:             imports,
		Invocations:         invocations,
		V128ResultHelperB64: helperB64,
		HostPrint:           hostPrint,
		DummyImportFunc:     dummyImportFunc,
		HostPrintResultKind: hostPrintResultKind,
	})
	if err != nil {
		return wabtInterpRunResult{}, err
	}
	tmpDir := filepath.Dir(wasmPath)
	payloadPath := filepath.Join(tmpDir, "wabt_interp_payload.json")
	if err := os.WriteFile(payloadPath, payloadBytes, 0o644); err != nil {
		return wabtInterpRunResult{}, fmt.Errorf("WriteFile %q failed: %w", payloadPath, err)
	}
	runnerPath := filepath.Join(".", "wabt_interp.js")

	out, err := exec.Command(nodePath, runnerPath, payloadPath).CombinedOutput()
	if err != nil {
		return wabtInterpRunResult{}, fmt.Errorf("node failed: %w\noutput:\n%s", err, out)
	}

	var payload struct {
		Stdout   []string           `json:"stdout"`
		Stderr   []string           `json:"stderr"`
		ExitCode int                `json:"exitCode"`
		Results  []wabtInterpResult `json:"results"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return wabtInterpRunResult{}, fmt.Errorf("decode node JSON: %w", err)
	}

	stdout := make([]string, 0, len(payload.Stdout)+len(payload.Results))
	nextStdout := 0
	for _, result := range payload.Results {
		for nextStdout < result.StdoutCount && nextStdout < len(payload.Stdout) {
			stdout = append(stdout, payload.Stdout[nextStdout])
			nextStdout++
		}
		line, err := formatWABTInterpResult(result)
		if err != nil {
			return wabtInterpRunResult{}, err
		}
		stdout = append(stdout, line)
	}
	stdout = append(stdout, payload.Stdout[nextStdout:]...)

	return wabtInterpRunResult{
		Stdout:   strings.Join(stdout, "\n"),
		Stderr:   strings.Join(payload.Stderr, "\n"),
		ExitCode: payload.ExitCode,
	}, nil
}

// formatWABTInterpResult converts one JSON result record from Node into the
// exact line format expected by WABT's interp tests.
func formatWABTInterpResult(result wabtInterpResult) (string, error) {
	label := result.Name + "()"
	if result.ArgText != "" {
		label = fmt.Sprintf("%s(%s)", result.Name, result.ArgText)
	}
	if result.Error != "" {
		return fmt.Sprintf("%s => error: %s", label, result.Error), nil
	}

	if result.ResultKind == "void" {
		return label + " =>", nil
	}

	var formatted string
	switch result.ResultKind {
	case "i32", "i64", "funcref", "externref":
		formatted = result.Value
	case "v128":
		formattedValue, err := formatWABTInterpV128(result.Value)
		if err != nil {
			return "", fmt.Errorf("parse v128 words for %q: %w", result.Name, err)
		}
		formatted = formattedValue
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
	if result.ResultKind == "v128" {
		return fmt.Sprintf("%s => v128 %s", label, formatted), nil
	}
	return fmt.Sprintf("%s => %s:%s", label, result.ResultKind, formatted), nil
}

func formatWABTInterpV128(wordsText string) (string, error) {
	parts := strings.Split(wordsText, ",")
	if len(parts) != 4 {
		return "", fmt.Errorf("want 4 words, got %d", len(parts))
	}
	words := make([]uint32, 0, 4)
	for _, part := range parts {
		word, err := strconv.ParseUint(part, 10, 32)
		if err != nil {
			return "", err
		}
		words = append(words, uint32(word))
	}
	return fmt.Sprintf("i32x4:0x%08x 0x%08x 0x%08x 0x%08x", words[0], words[1], words[2], words[3]), nil
}

var (
	wabtInterpV128ResultHelperOnce sync.Once
	wabtInterpV128ResultHelperB64  string
	wabtInterpV128ResultHelperErr  error
)

func wabtInterpV128ResultHelperBase64(exports []wabtInterpExport) (string, error) {
	needsHelper := false
	for _, exp := range exports {
		if exp.Kind == "func" && exp.ResultKind == "v128" {
			needsHelper = true
			break
		}
	}
	if !needsHelper {
		return "", nil
	}

	wabtInterpV128ResultHelperOnce.Do(func() {
		const helperWAT = `(module
  (import "m" "f" (func $f (result v128)))
  (memory (export "mem") 1)
  (func (export "call")
    (i32.const 0)
    (call $f)
    v128.store))`
		wasmBytes, err := watgo.CompileWATToWASM([]byte(helperWAT))
		if err != nil {
			wabtInterpV128ResultHelperErr = err
			return
		}
		wabtInterpV128ResultHelperB64 = base64.StdEncoding.EncodeToString(wasmBytes)
	})
	return wabtInterpV128ResultHelperB64, wabtInterpV128ResultHelperErr
}

// wabtInterpStdoutMatches compares reconstructed output against WABT's
// expected stdout.
//
// Most lines are compared byte-for-byte. The only special case is
// reference-valued result lines such as:
//
//	ref_null_func() => funcref:0
//	ref_func() => funcref:5
//
// WABT's interpreter prints implementation-specific non-null ref identities,
// while Node only gives us enough information to distinguish null from
// non-null and to preserve stable funcref identity within the module. For
// those lines, the comparison keeps the export name and ref kind exact but
// treats any two non-zero ids as equivalent.
func wabtInterpStdoutMatches(got, want string) bool {
	if got == want {
		return true
	}

	gotLines := strings.Split(got, "\n")
	wantLines := strings.Split(want, "\n")
	if len(gotLines) != len(wantLines) {
		return false
	}
	for i := range gotLines {
		if gotLines[i] == wantLines[i] {
			continue
		}
		if wabtInterpV128LineMatches(gotLines[i], wantLines[i]) {
			continue
		}
		if !wabtInterpRefLineMatches(gotLines[i], wantLines[i]) {
			return false
		}
	}
	return true
}

// wabtInterpRefLineMatches compares one reference-valued output line.
//
// Example:
//
//	got:  ref_func() => funcref:2
//	want: ref_func() => funcref:5
//
// This still matches, because both lines describe the same export and ref
// kind, and both ids are non-zero. Null references remain exact:
//
//	funcref:0 only matches funcref:0
//	externref:0 only matches externref:0
func wabtInterpRefLineMatches(got, want string) bool {
	gotName, gotKind, gotValue, ok := splitWABTRefLine(got)
	if !ok {
		return false
	}
	wantName, wantKind, wantValue, ok := splitWABTRefLine(want)
	if !ok {
		return false
	}
	if gotName != wantName || gotKind != wantKind {
		return false
	}
	if gotValue == wantValue {
		return true
	}
	return gotValue != "0" && wantValue != "0"
}

// splitWABTRefLine extracts `name`, `kind`, and `value` from one
// reference-valued output line.
//
// For example:
//
//	"ref_func() => funcref:5"    -> ("ref_func", "funcref", "5", true)
//	"ref_null_extern() => externref:0" -> ("ref_null_extern", "externref", "0", true)
//
// Non-reference lines return ok=false.
func splitWABTRefLine(line string) (name, kind, value string, ok bool) {
	prefix, rest, found := strings.Cut(line, "() => ")
	if !found {
		return "", "", "", false
	}
	kind, value, found = strings.Cut(rest, ":")
	if !found {
		return "", "", "", false
	}
	if kind != "funcref" && kind != "externref" {
		return "", "", "", false
	}
	return prefix, kind, value, true
}

func wabtInterpV128LineMatches(got, want string) bool {
	gotName, gotWords, ok := splitWABTV128Line(got)
	if !ok {
		return false
	}
	wantName, wantWords, ok := splitWABTV128Line(want)
	if !ok || gotName != wantName {
		return false
	}

	switch {
	case strings.Contains(gotName, "f64x2"), strings.Contains(wantName, "f64x2"):
		for i := 0; i < 4; i += 2 {
			if gotWords[i] == wantWords[i] && gotWords[i+1] == wantWords[i+1] {
				continue
			}
			gotBits := uint64(gotWords[i]) | (uint64(gotWords[i+1]) << 32)
			wantBits := uint64(wantWords[i]) | (uint64(wantWords[i+1]) << 32)
			if !bothF64NaNBits(gotBits, wantBits) {
				return false
			}
		}
		return true
	case strings.Contains(gotName, "f32x4"), strings.Contains(wantName, "f32x4"):
		for i := 0; i < 4; i++ {
			if gotWords[i] == wantWords[i] {
				continue
			}
			if !bothF32NaNBits(gotWords[i], wantWords[i]) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func splitWABTV128Line(line string) (name string, words [4]uint32, ok bool) {
	prefix, rest, found := strings.Cut(line, "() => v128 i32x4:")
	if !found {
		return "", words, false
	}
	parts := strings.Fields(strings.TrimSpace(rest))
	if len(parts) != 4 {
		return "", words, false
	}
	for i, part := range parts {
		word, err := strconv.ParseUint(strings.TrimPrefix(part, "0x"), 16, 32)
		if err != nil {
			return "", words, false
		}
		words[i] = uint32(word)
	}
	return prefix, words, true
}

func bothF32NaNBits(a, b uint32) bool {
	return isF32NaNBits(a) && isF32NaNBits(b)
}

func isF32NaNBits(bits uint32) bool {
	return bits&0x7f800000 == 0x7f800000 && bits&0x007fffff != 0
}

func bothF64NaNBits(a, b uint64) bool {
	return isF64NaNBits(a) && isF64NaNBits(b)
}

func isF64NaNBits(bits uint64) bool {
	return bits&0x7ff0000000000000 == 0x7ff0000000000000 && bits&0x000fffffffffffff != 0
}
