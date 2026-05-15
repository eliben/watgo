package tests

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/eliben/watgo/wasmir"
	"github.com/eliben/watgo/wasmvm"
)

// wabtInterpWasmVMFixtures lists fixtures covered by the wasmvm backend while
// its instruction support is still growing.
var wabtInterpWasmVMFixtures = []string{
	"basic.txt",
	"call.txt",
	"callimport-zero-args.txt",
}

// wabtInterpWasmVMBackend returns the wasmvm-backed WABT interp execution
// backend.
func wabtInterpWasmVMBackend() wabtInterpBackend {
	return wabtInterpBackend{
		name:     "wasmvm",
		fixtures: wabtInterpWasmVMFixtures,
		run:      runWABTInterpWasmVMFixture,
	}
}

// runWABTInterpWasmVMFixture executes one compiled fixture through wasmvm.
func runWABTInterpWasmVMFixture(t *testing.T, fixture wabtInterpCompiledFixture) (wabtInterpRunResult, error) {
	t.Helper()

	exports, err := wabtInterpExports(fixture.m)
	if err != nil {
		return wabtInterpRunResult{}, fmt.Errorf("wabtInterpExports %q failed: %w", fixture.path, err)
	}

	hostPrintResultKind, err := wabtInterpHostPrintResultKind(fixture.m)
	if err != nil {
		return wabtInterpRunResult{}, fmt.Errorf("wabtInterpHostPrintResultKind %q failed: %w", fixture.path, err)
	}

	imports, err := wabtInterpImports(fixture.m)
	if err != nil {
		return wabtInterpRunResult{}, fmt.Errorf("wabtInterpImports %q failed: %w", fixture.path, err)
	}

	return runWABTInterpWasmVM(fixture.m, exports, imports, fixture.tc.runArgs, hostPrintResultKind)
}

// runWABTInterpWasmVM instantiates m with wasmvm and executes the requested
// WABT run-interp exports.
func runWABTInterpWasmVM(m *wasmir.Module, exports []wabtInterpExport, imports []wabtInterpImport, runArgs []string, hostPrintResultKind string) (wabtInterpRunResult, error) {
	invocations, hostPrint, dummyImportFunc, err := wabtInterpInvocations(exports, runArgs)
	if err != nil {
		return wabtInterpRunResult{}, err
	}
	if runResult, ok := wabtInterpValidateInvocations(exports, invocations); ok {
		return runResult, nil
	}

	stdout := []string{}
	vmImports, err := wabtInterpWasmVMImports(imports, hostPrint, dummyImportFunc, hostPrintResultKind, &stdout)
	if err != nil {
		return wabtInterpRunResult{}, err
	}

	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(m, vmImports)
	if err != nil {
		return wabtInterpRunResult{Stderr: normalizeWABTInterpWasmVMError(err), ExitCode: 1}, nil
	}

	exportMap := make(map[string]wabtInterpExport, len(exports))
	for _, exp := range exports {
		exportMap[exp.Name] = exp
	}

	results := make([]wabtInterpResult, 0, len(invocations))
	for _, invocation := range invocations {
		entry, ok := exportMap[invocation.ExportName]
		if !ok {
			return wabtInterpRunResult{Stderr: "unknown export " + invocation.ExportName, ExitCode: 1}, nil
		}
		if entry.Kind != "func" {
			return wabtInterpRunResult{Stdout: "Export '" + invocation.ExportName + "' is not a function", ExitCode: 1}, nil
		}
		fn, ok := inst.ExportedFunc(entry.Name)
		if !ok {
			return wabtInterpRunResult{Stderr: "unknown export " + invocation.ExportName, ExitCode: 1}, nil
		}

		args, argText, err := wabtInterpWasmVMArgs(invocation.Args)
		if err != nil {
			return wabtInterpRunResult{}, err
		}
		values, callErr := fn.Call(args...)
		result := wabtInterpResult{
			Name:        entry.Name,
			ResultKind:  entry.ResultKind,
			ArgText:     argText,
			StdoutCount: len(stdout),
		}
		if callErr != nil {
			result.Error = normalizeWABTInterpWasmVMError(callErr)
			results = append(results, result)
			continue
		}
		result.Value, err = wabtInterpWasmVMResultValue(entry.ResultKind, values)
		if err != nil {
			return wabtInterpRunResult{}, err
		}
		results = append(results, result)
	}

	return wabtInterpRunResult{
		Stdout:   wabtInterpMergeStdout(stdout, results),
		ExitCode: 0,
	}, nil
}

// wabtInterpWasmVMImports builds the synthetic host imports requested by WABT
// run-interp flags.
func wabtInterpWasmVMImports(imports []wabtInterpImport, hostPrint bool, dummyImportFunc bool, hostPrintResultKind string, stdout *[]string) (wasmvm.Imports, error) {
	var out wasmvm.Imports
	add := func(module string, name string, host wasmvm.HostFunc) error {
		if out == nil {
			out = make(wasmvm.Imports)
		}
		if out[module] == nil {
			out[module] = make(map[string]wasmvm.Extern)
		}
		if _, exists := out[module][name]; exists {
			return fmt.Errorf("duplicate synthetic import %q.%q", module, name)
		}
		out[module][name] = host
		return nil
	}

	for _, imported := range imports {
		imported := imported
		switch {
		case hostPrint && imported.Module == "host" && imported.Name == "print":
			host, err := wabtInterpWasmVMHostPrint(imported, hostPrintResultKind, stdout)
			if err != nil {
				return nil, err
			}
			if err := add(imported.Module, imported.Name, host); err != nil {
				return nil, err
			}
		case dummyImportFunc:
			host, err := wabtInterpWasmVMDummyImport(imported, stdout)
			if err != nil {
				return nil, err
			}
			if err := add(imported.Module, imported.Name, host); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// wabtInterpWasmVMHostPrint returns a host.print shim for the wasmvm backend.
func wabtInterpWasmVMHostPrint(imported wabtInterpImport, hostPrintResultKind string, stdout *[]string) (wasmvm.HostFunc, error) {
	params, err := wabtInterpWasmVMValueTypes(imported.ParamKinds)
	if err != nil {
		return wasmvm.HostFunc{}, err
	}
	results, err := wabtInterpWasmVMResultTypes(imported.ResultKind)
	if err != nil {
		return wasmvm.HostFunc{}, err
	}
	return wasmvm.NewHostFunc(params, results, func(_ *wasmvm.Context, args []wasmvm.Value) ([]wasmvm.Value, error) {
		formattedArgs := make([]string, 0, len(args))
		for _, arg := range args {
			formattedArgs = append(formattedArgs, wabtInterpWasmVMHostPrintArg(arg))
		}
		if hostPrintResultKind == "void" || hostPrintResultKind == "" {
			*stdout = append(*stdout, "called host host.print("+strings.Join(formattedArgs, ", ")+") =>")
		} else {
			*stdout = append(*stdout, "called host host.print("+strings.Join(formattedArgs, ", ")+") => "+hostPrintResultKind+":0")
		}
		return wabtInterpWasmVMZeroResults(imported.ResultKind)
	}), nil
}

// wabtInterpWasmVMDummyImport returns a dummy host function for the wasmvm
// backend.
func wabtInterpWasmVMDummyImport(imported wabtInterpImport, stdout *[]string) (wasmvm.HostFunc, error) {
	params, err := wabtInterpWasmVMValueTypes(imported.ParamKinds)
	if err != nil {
		return wasmvm.HostFunc{}, err
	}
	results, err := wabtInterpWasmVMResultTypes(imported.ResultKind)
	if err != nil {
		return wasmvm.HostFunc{}, err
	}
	return wasmvm.NewHostFunc(params, results, func(_ *wasmvm.Context, args []wasmvm.Value) ([]wasmvm.Value, error) {
		formattedArgs := make([]string, 0, len(args))
		for i, arg := range args {
			formattedArgs = append(formattedArgs, wabtInterpWasmVMFormatValueByKind(imported.ParamKinds[i], arg))
		}
		suffix := ""
		if imported.ResultKind != "void" {
			suffix = " " + imported.ResultKind + ":0"
		}
		*stdout = append(*stdout, "called host "+imported.Module+"."+imported.Name+"("+strings.Join(formattedArgs, ", ")+") =>"+suffix)
		return wabtInterpWasmVMZeroResults(imported.ResultKind)
	}), nil
}

// wabtInterpWasmVMArgs decodes one WABT invocation argument list.
func wabtInterpWasmVMArgs(args []wabtInterpInvocationArg) ([]wasmvm.Value, string, error) {
	values := make([]wasmvm.Value, 0, len(args))
	argText := make([]string, 0, len(args))
	for _, arg := range args {
		v, err := wabtInterpWasmVMArg(arg)
		if err != nil {
			return nil, "", err
		}
		values = append(values, v)
		argText = append(argText, arg.Kind+":"+arg.Text)
	}
	return values, strings.Join(argText, ", "), nil
}

// wabtInterpWasmVMArg decodes one WABT invocation argument.
func wabtInterpWasmVMArg(arg wabtInterpInvocationArg) (wasmvm.Value, error) {
	switch arg.Kind {
	case "i32":
		v, err := strconv.ParseInt(arg.Text, 10, 32)
		if err != nil {
			return wasmvm.Value{}, err
		}
		return wasmvm.I32(int32(v)), nil
	case "i64":
		v, err := strconv.ParseInt(arg.Text, 10, 64)
		if err != nil {
			return wasmvm.Value{}, err
		}
		return wasmvm.I64(v), nil
	case "f32":
		v, err := strconv.ParseFloat(arg.Text, 32)
		if err != nil {
			return wasmvm.Value{}, err
		}
		return wasmvm.F32(float32(v)), nil
	case "f64":
		v, err := strconv.ParseFloat(arg.Text, 64)
		if err != nil {
			return wasmvm.Value{}, err
		}
		return wasmvm.F64(v), nil
	default:
		return wasmvm.Value{}, fmt.Errorf("unsupported invocation arg kind: %s", arg.Kind)
	}
}

// wabtInterpWasmVMValueTypes converts WABT value-kind strings to wasm value
// types.
func wabtInterpWasmVMValueTypes(kinds []string) ([]wasmir.ValueType, error) {
	types := make([]wasmir.ValueType, 0, len(kinds))
	for _, kind := range kinds {
		vt, err := wabtInterpWasmVMValueType(kind)
		if err != nil {
			return nil, err
		}
		types = append(types, vt)
	}
	return types, nil
}

// wabtInterpWasmVMResultTypes returns the result type list for resultKind.
func wabtInterpWasmVMResultTypes(resultKind string) ([]wasmir.ValueType, error) {
	if resultKind == "void" {
		return nil, nil
	}
	vt, err := wabtInterpWasmVMValueType(resultKind)
	if err != nil {
		return nil, err
	}
	return []wasmir.ValueType{vt}, nil
}

// wabtInterpWasmVMValueType converts one WABT value-kind string to a wasm value
// type.
func wabtInterpWasmVMValueType(kind string) (wasmir.ValueType, error) {
	switch kind {
	case "i32":
		return wasmir.ValueTypeI32, nil
	case "i64":
		return wasmir.ValueTypeI64, nil
	case "f32":
		return wasmir.ValueTypeF32, nil
	case "f64":
		return wasmir.ValueTypeF64, nil
	case "funcref":
		return wasmir.RefTypeFunc(true), nil
	case "externref":
		return wasmir.RefTypeExtern(true), nil
	default:
		return wasmir.ValueType{}, fmt.Errorf("unsupported value kind %q", kind)
	}
}

// wabtInterpWasmVMZeroResults returns the zero host result for resultKind.
func wabtInterpWasmVMZeroResults(resultKind string) ([]wasmvm.Value, error) {
	if resultKind == "void" {
		return nil, nil
	}
	vt, err := wabtInterpWasmVMValueType(resultKind)
	if err != nil {
		return nil, err
	}
	return []wasmvm.Value{{Type: vt}}, nil
}

// wabtInterpWasmVMResultValue formats wasmvm call results in the raw form
// consumed by formatWABTInterpResult.
func wabtInterpWasmVMResultValue(resultKind string, values []wasmvm.Value) (string, error) {
	if resultKind == "void" {
		if len(values) != 0 {
			return "", fmt.Errorf("got %d results, want 0", len(values))
		}
		return "", nil
	}
	if len(values) != 1 {
		return "", fmt.Errorf("got %d results, want 1", len(values))
	}
	v := values[0]
	switch resultKind {
	case "i32":
		return strconv.FormatUint(uint64(uint32(v.I32)), 10), nil
	case "i64":
		return strconv.FormatUint(uint64(v.I64), 10), nil
	case "f32":
		return strconv.FormatUint(uint64(math.Float32bits(v.F32)), 10), nil
	case "f64":
		return strconv.FormatUint(math.Float64bits(v.F64), 10), nil
	case "funcref":
		if v.Ref.Kind == 0 {
			return "0", nil
		}
		return strconv.FormatUint(uint64(v.Ref.FuncIndex)+1, 10), nil
	case "externref":
		if v.Ref.Kind == 0 {
			return "0", nil
		}
		return "1", nil
	default:
		return "", fmt.Errorf("unsupported result kind %q", resultKind)
	}
}

// wabtInterpWasmVMHostPrintArg formats a host-print argument the same way as
// the Node backend's host-print shim.
func wabtInterpWasmVMHostPrintArg(v wasmvm.Value) string {
	switch v.Type.Kind {
	case wasmir.ValueKindI64:
		return "i64:" + strconv.FormatUint(uint64(v.I64), 10)
	case wasmir.ValueKindRef:
		if v.Ref.Kind == 0 {
			return "externref:0"
		}
		return "externref:1"
	default:
		return "i32:" + strconv.FormatUint(uint64(uint32(v.I32)), 10)
	}
}

// wabtInterpWasmVMFormatValueByKind formats a dummy-import argument according
// to its declared type.
func wabtInterpWasmVMFormatValueByKind(kind string, v wasmvm.Value) string {
	switch kind {
	case "i32":
		return "i32:" + strconv.FormatUint(uint64(uint32(v.I32)), 10)
	case "i64":
		return "i64:" + strconv.FormatUint(uint64(v.I64), 10)
	case "f32":
		return "f32:" + strconv.FormatFloat(float64(v.F32), 'f', 6, 32)
	case "f64":
		return "f64:" + strconv.FormatFloat(v.F64, 'f', 6, 64)
	case "funcref":
		if v.Ref.Kind == 0 {
			return "funcref:0"
		}
		return "funcref:1"
	case "externref":
		if v.Ref.Kind == 0 {
			return "externref:0"
		}
		return "externref:1"
	default:
		return kind + ":?"
	}
}

// wabtInterpMergeStdout interleaves host stdout and invocation result lines.
func wabtInterpMergeStdout(stdout []string, results []wabtInterpResult) string {
	lines := make([]string, 0, len(stdout)+len(results))
	nextStdout := 0
	for _, result := range results {
		for nextStdout < result.StdoutCount && nextStdout < len(stdout) {
			lines = append(lines, stdout[nextStdout])
			nextStdout++
		}
		line, err := formatWABTInterpResult(result)
		if err != nil {
			lines = append(lines, "error: "+err.Error())
			continue
		}
		lines = append(lines, line)
	}
	lines = append(lines, stdout[nextStdout:]...)
	return strings.Join(lines, "\n")
}

// normalizeWABTInterpWasmVMError maps wasmvm errors to WABT-style trap text
// where the existing harness expects normalized wording.
func normalizeWABTInterpWasmVMError(err error) string {
	message := err.Error()
	switch {
	case strings.Contains(message, "divide by zero"):
		return "integer divide by zero"
	case strings.Contains(message, "table access out of bounds"):
		return "undefined table index"
	case strings.Contains(message, "indirect call"):
		return "indirect call signature mismatch"
	case strings.Contains(message, "unreachable executed"):
		return "unreachable executed"
	default:
		return message
	}
}
