package tests

import (
	"flag"
	"fmt"
	"maps"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eliben/watgo"
	"github.com/eliben/watgo/internal/binaryformat"
	"github.com/eliben/watgo/internal/textformat"
	"github.com/eliben/watgo/internal/validate"
	"github.com/eliben/watgo/wasmir"
	"github.com/eliben/watgo/wasmvm"
)

var wasmSpecWasmvmScriptsFlag = flag.String(
	"wasmspec.wasmvm-scripts",
	"",
	"comma-separated wasmspec scripts to run through the wasmvm backend",
)

type wasmSpecWasmvmRunner struct {
	rt                 *wasmvm.Runtime
	imports            wasmvm.Imports
	current            *wasmvm.ModuleInstance
	currentMeta        *moduleMetadata
	currentSynthetic   *syntheticModule
	currentRuntimeName string
	instances          map[string]*wasmvm.ModuleInstance
	moduleDefs         map[string]*syntheticDefinition
	moduleMeta         map[string]*moduleMetadata
	moduleAlias        map[string]string
	syntheticInstances map[string]*syntheticInstance
	syntheticModules   map[string]*syntheticModule
	nextTagID          uint64
}

// wasmSpecWasmvmBackend returns the initial wasmvm-backed wasmspec runner.
func wasmSpecWasmvmBackend(t *testing.T) wasmSpecBackend {
	t.Helper()

	return wasmSpecBackend{
		name:    "wasmvm",
		scripts: wasmSpecWasmvmSelectedScripts(t),
		run:     runWasmSpecWasmvmScript,
	}
}

// wasmSpecWasmvmSelectedScripts returns the wasmvm script list, optionally
// overridden by a comma-separated test flag for coverage triage.
func wasmSpecWasmvmSelectedScripts(t *testing.T) []string {
	t.Helper()

	override := strings.TrimSpace(*wasmSpecWasmvmScriptsFlag)
	if override == "" {
		return wasmSpecWasmvmDiscoveredScripts(t)
	}
	var scripts []string
	for entry := range strings.SplitSeq(override, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		scripts = append(scripts, entry)
	}
	return scripts
}

// wasmSpecWasmvmDiscoveredScripts returns all checked-in wasmspec scripts.
func wasmSpecWasmvmDiscoveredScripts(t *testing.T) []string {
	t.Helper()

	scripts, err := findWasmSpecScripts(wasmSpecScriptsDir)
	if err != nil {
		t.Fatalf("findWasmSpecScripts %q failed: %v", wasmSpecScriptsDir, err)
	}
	for i, script := range scripts {
		scripts[i] = filepath.Join(wasmSpecScriptsDir, script)
	}
	return scripts
}

// newWasmSpecWasmvmRunner creates an empty wasmvm script runner.
func newWasmSpecWasmvmRunner() *wasmSpecWasmvmRunner {
	return &wasmSpecWasmvmRunner{
		rt:                 wasmvm.NewRuntime(),
		imports:            wasmSpecWasmvmSpectestImports(),
		instances:          map[string]*wasmvm.ModuleInstance{},
		moduleDefs:         map[string]*syntheticDefinition{},
		moduleMeta:         map[string]*moduleMetadata{},
		moduleAlias:        map[string]string{},
		syntheticInstances: map[string]*syntheticInstance{},
		syntheticModules:   map[string]*syntheticModule{},
	}
}

// wasmSpecWasmvmSpectestImports returns the extern imports exposed by the spec
// harness's synthetic "spectest" module.
func wasmSpecWasmvmSpectestImports() wasmvm.Imports {
	return wasmvm.Imports{
		"spectest": map[string]wasmvm.Extern{
			"print":         wasmSpecWasmvmHostFunc(nil),
			"print_i32":     wasmSpecWasmvmHostFunc([]wasmir.ValueType{wasmir.ValueTypeI32}),
			"print_i64":     wasmSpecWasmvmHostFunc([]wasmir.ValueType{wasmir.ValueTypeI64}),
			"print_f32":     wasmSpecWasmvmHostFunc([]wasmir.ValueType{wasmir.ValueTypeF32}),
			"print_f64":     wasmSpecWasmvmHostFunc([]wasmir.ValueType{wasmir.ValueTypeF64}),
			"print_i32_f32": wasmSpecWasmvmHostFunc([]wasmir.ValueType{wasmir.ValueTypeI32, wasmir.ValueTypeF32}),
			"print_f64_f64": wasmSpecWasmvmHostFunc([]wasmir.ValueType{wasmir.ValueTypeF64, wasmir.ValueTypeF64}),
			"table":         wasmSpecWasmvmTable(wasmir.ValueTypeI32, 10, new(uint64(20))),
			"table64":       wasmSpecWasmvmTable(wasmir.ValueTypeI64, 0, nil),
			"memory":        wasmSpecWasmvmMemory(wasmir.ValueTypeI32, 1, new(uint64(2))),
			"global_i32":    wasmSpecWasmvmGlobal(wasmvm.I32(666)),
			"global_i64":    wasmSpecWasmvmGlobal(wasmvm.I64(666)),
			"global_f32":    wasmSpecWasmvmGlobal(wasmvm.F32(666.6)),
			"global_f64":    wasmSpecWasmvmGlobal(wasmvm.F64(666.6)),
		},
	}
}

// wasmSpecWasmvmHostFunc returns a no-op host function with params and no
// results.
func wasmSpecWasmvmHostFunc(params []wasmir.ValueType) *wasmvm.HostFunc {
	return wasmvm.NewHostFunc(params, nil, func(ctx *wasmvm.Context, args []wasmvm.Value) ([]wasmvm.Value, error) {
		return nil, nil
	})
}

// wasmSpecWasmvmMemory returns a spectest memory import.
func wasmSpecWasmvmMemory(addressType wasmir.ValueType, min uint64, max *uint64) *wasmvm.Memory {
	mem, err := wasmvm.NewMemory(wasmir.Memory{AddressType: addressType, Min: min, Max: max})
	if err != nil {
		panic(err)
	}
	return mem
}

// wasmSpecWasmvmTable returns a spectest table import.
func wasmSpecWasmvmTable(addressType wasmir.ValueType, min uint64, max *uint64) *wasmvm.Table {
	table, err := wasmvm.NewTable(wasmir.Table{
		AddressType: addressType,
		Min:         min,
		Max:         max,
		RefType:     wasmir.RefTypeFunc(true),
	})
	if err != nil {
		panic(err)
	}
	return table
}

// wasmSpecWasmvmGlobal returns an immutable spectest global.
func wasmSpecWasmvmGlobal(value wasmvm.Value) *wasmvm.Global {
	global, err := wasmvm.NewGlobal(value, false)
	if err != nil {
		panic(err)
	}
	return global
}

// runWasmSpecWasmvmScript executes parsed wasmspec commands through wasmvm.
func runWasmSpecWasmvmScript(t *testing.T, scriptPath string, commands []scriptCommand, opts runOptions) []commandResult {
	t.Helper()

	runner := newWasmSpecWasmvmRunner()
	return runner.run(commands, opts)
}

// run executes commands in script order and returns one result per command.
func (r *wasmSpecWasmvmRunner) run(commands []scriptCommand, opts runOptions) []commandResult {
	results := make([]commandResult, 0, len(commands))
	for i, cmd := range commands {
		if opts.progress != nil {
			opts.progress(i, len(commands), cmd)
		}
		start := time.Now()
		res := commandResult{
			index: i,
			kind:  cmd.kind,
			loc:   cmd.loc,
		}
		r.runCommand(&res, cmd, opts)
		if opts.progressDone != nil {
			opts.progressDone(i, len(commands), cmd, res, time.Since(start))
		}
		results = append(results, res)
	}
	return results
}

// runCommand dispatches one parsed wasmspec command to wasmvm behavior.
func (r *wasmSpecWasmvmRunner) runCommand(res *commandResult, cmd scriptCommand, opts runOptions) {
	switch cmd.kind {
	case commandModule:
		r.runModule(res, cmd)
	case commandModuleInstance:
		r.runModuleInstance(res, cmd)
	case commandInvoke:
		r.runInvoke(res, cmd)
	case commandRegister:
		r.runRegister(res, cmd)
	case commandAssertReturn:
		r.runAssertReturn(res, cmd)
	case commandAssertTrap:
		r.runAssertTrap(res, cmd)
	case commandAssertException:
		r.runAssertException(res, cmd)
	case commandAssertExhaustion:
		r.runAssertExhaustion(res, cmd)
	case commandAssertUnlinkable:
		r.runAssertUnlinkable(res, cmd, opts)
	case commandAssertInvalid:
		r.runAssertInvalid(res, cmd, opts)
	case commandAssertMalformed:
		r.runAssertMalformed(res, cmd, opts)
	default:
		res.status = false
		res.detail = fmt.Sprintf("unsupported wasmvm command kind %q", cmd.kind)
	}
}

// runModule compiles and instantiates a top-level module command.
func (r *wasmSpecWasmvmRunner) runModule(res *commandResult, cmd scriptCommand) {
	if isModuleDefinitionExpr(cmd.moduleExpr) {
		def, ok, err := buildSyntheticDefinition(cmd.moduleExpr)
		if err != nil {
			res.status = false
			res.detail = fmt.Sprintf("module definition parse failed: %v", err)
			return
		}
		if ok {
			r.moduleDefs[cmd.moduleName] = def
			r.current = nil
			r.currentMeta = nil
			r.currentSynthetic = nil
			r.currentRuntimeName = cmd.moduleName
			res.status = true
			return
		}
	}
	if synth, ok, err := r.buildSyntheticConsumerModule(cmd.moduleExpr); err != nil {
		res.status = false
		res.detail = fmt.Sprintf("module synth failed: %v", err)
		return
	} else if ok {
		runtimeName := runtimeModuleName(cmd.moduleName)
		r.current = nil
		r.currentMeta = synth.meta
		r.currentSynthetic = synth
		r.currentRuntimeName = runtimeName
		if cmd.moduleName != "" {
			r.syntheticModules[runtimeName] = synth
			r.moduleMeta[runtimeName] = synth.meta
			r.moduleAlias[cmd.moduleName] = runtimeName
		}
		res.status = true
		return
	}

	m, meta, err := compileWasmSpecCommandForWasmvm(cmd)
	if err != nil {
		res.status = false
		res.detail = fmt.Sprintf("module compile failed: %v", err)
		return
	}

	inst, err := r.instantiate(m)
	if err != nil {
		res.status = false
		res.detail = fmt.Sprintf("module instantiate failed: %v", err)
		return
	}

	runtimeName := runtimeModuleName(cmd.moduleName)
	r.current = inst
	r.currentMeta = meta
	r.currentSynthetic = nil
	r.currentRuntimeName = runtimeName
	r.instances[runtimeName] = inst
	r.moduleMeta[runtimeName] = meta
	if cmd.moduleName != "" {
		r.moduleAlias[cmd.moduleName] = runtimeName
	}
	r.bindFunctionExports(runtimeName, inst, m)
	r.bindGlobalExports(runtimeName, inst, m)
	r.bindMemoryExports(runtimeName, inst, m)
	r.bindTableExports(runtimeName, inst, m)
	r.bindTagExports(runtimeName, inst, m)
	res.status = true
}

// runModuleInstance handles "(module instance $I $M)" for harness synthetic
// module definitions.
func (r *wasmSpecWasmvmRunner) runModuleInstance(res *commandResult, cmd scriptCommand) {
	def, ok := r.moduleDefs[cmd.instanceOf]
	if !ok {
		res.status = false
		res.detail = fmt.Sprintf("module definition %q not found", cmd.instanceOf)
		return
	}
	inst := instantiateSyntheticDefinition(def, &r.nextTagID)
	r.syntheticInstances[cmd.moduleName] = inst
	r.moduleAlias[cmd.moduleName] = cmd.moduleName
	r.current = nil
	r.currentMeta = nil
	r.currentSynthetic = nil
	r.currentRuntimeName = cmd.moduleName
	res.status = true
}

// runRegister aliases an already-instantiated wasmvm module for later named
// actions.
func (r *wasmSpecWasmvmRunner) runRegister(res *commandResult, cmd scriptCommand) {
	if cmd.registerName == "" {
		res.status = false
		res.detail = "register command missing name"
		return
	}
	sourceName := cmd.registerFrom
	if sourceName == "" {
		sourceName = r.currentRuntimeName
	}
	if synthInst, ok := r.lookupSyntheticInstance(sourceName); ok {
		r.syntheticInstances[cmd.registerName] = synthInst
		r.moduleAlias[cmd.registerName] = cmd.registerName
		if cmd.registerFrom != "" {
			r.moduleAlias[cmd.registerFrom] = cmd.registerName
		}
		res.status = true
		return
	}
	if synth, ok := r.lookupSyntheticModule(sourceName); ok {
		r.syntheticModules[cmd.registerName] = synth
		r.moduleMeta[cmd.registerName] = synth.meta
		r.moduleAlias[cmd.registerName] = cmd.registerName
		if cmd.registerFrom != "" {
			r.moduleAlias[cmd.registerFrom] = cmd.registerName
		}
		if sourceName == r.currentRuntimeName {
			r.current = nil
			r.currentMeta = synth.meta
			r.currentSynthetic = synth
			r.currentRuntimeName = cmd.registerName
		}
		res.status = true
		return
	}
	inst, meta, err := r.lookupInstance(sourceName)
	if err != nil {
		res.status = false
		res.detail = fmt.Sprintf("register source failed: %v", err)
		return
	}
	r.instances[cmd.registerName] = inst
	r.moduleMeta[cmd.registerName] = meta
	r.moduleAlias[cmd.registerName] = cmd.registerName
	r.copyRegisteredImports(cmd.registerName, sourceName)
	if cmd.registerFrom != "" {
		r.moduleAlias[cmd.registerFrom] = cmd.registerName
	}
	if sourceName == r.currentRuntimeName {
		r.current = inst
		r.currentMeta = meta
		r.currentSynthetic = nil
		r.currentRuntimeName = cmd.registerName
	}
	res.status = true
}

// bindFunctionExports exposes function exports under runtimeName for later
// function imports in the same spec script.
func (r *wasmSpecWasmvmRunner) bindFunctionExports(runtimeName string, inst *wasmvm.ModuleInstance, m *wasmir.Module) {
	for _, exp := range m.Exports {
		if exp.Kind != wasmir.ExternalKindFunction {
			continue
		}
		fn, ok := inst.ExportedFunc(exp.Name)
		if !ok {
			continue
		}
		if r.imports[runtimeName] == nil {
			r.imports[runtimeName] = map[string]wasmvm.Extern{}
		}
		r.imports[runtimeName][exp.Name] = fn
	}
}

// bindGlobalExports exposes global exports under runtimeName for later global
// imports in the same spec script.
func (r *wasmSpecWasmvmRunner) bindGlobalExports(runtimeName string, inst *wasmvm.ModuleInstance, m *wasmir.Module) {
	for _, exp := range m.Exports {
		if exp.Kind != wasmir.ExternalKindGlobal {
			continue
		}
		global, ok := inst.ExportedGlobal(exp.Name)
		if !ok {
			continue
		}
		if r.imports[runtimeName] == nil {
			r.imports[runtimeName] = map[string]wasmvm.Extern{}
		}
		r.imports[runtimeName][exp.Name] = global
	}
}

// bindMemoryExports exposes memory exports under runtimeName for later memory
// imports in the same spec script.
func (r *wasmSpecWasmvmRunner) bindMemoryExports(runtimeName string, inst *wasmvm.ModuleInstance, m *wasmir.Module) {
	for _, exp := range m.Exports {
		if exp.Kind != wasmir.ExternalKindMemory {
			continue
		}
		mem, ok := inst.ExportedMemory(exp.Name)
		if !ok {
			continue
		}
		if r.imports[runtimeName] == nil {
			r.imports[runtimeName] = map[string]wasmvm.Extern{}
		}
		r.imports[runtimeName][exp.Name] = mem
	}
}

// bindTableExports exposes table exports under runtimeName for later table
// imports in the same spec script.
func (r *wasmSpecWasmvmRunner) bindTableExports(runtimeName string, inst *wasmvm.ModuleInstance, m *wasmir.Module) {
	for _, exp := range m.Exports {
		if exp.Kind != wasmir.ExternalKindTable {
			continue
		}
		table, ok := inst.ExportedTable(exp.Name)
		if !ok {
			continue
		}
		if r.imports[runtimeName] == nil {
			r.imports[runtimeName] = map[string]wasmvm.Extern{}
		}
		r.imports[runtimeName][exp.Name] = table
	}
}

// bindTagExports exposes tag exports under runtimeName for later tag imports
// in the same spec script.
func (r *wasmSpecWasmvmRunner) bindTagExports(runtimeName string, inst *wasmvm.ModuleInstance, m *wasmir.Module) {
	for _, exp := range m.Exports {
		if exp.Kind != wasmir.ExternalKindTag {
			continue
		}
		tag, ok := inst.ExportedTag(exp.Name)
		if !ok {
			continue
		}
		if r.imports[runtimeName] == nil {
			r.imports[runtimeName] = map[string]wasmvm.Extern{}
		}
		r.imports[runtimeName][exp.Name] = tag
	}
}

// copyRegisteredImports aliases importable exports when a spec register
// command gives a module a new import module name.
func (r *wasmSpecWasmvmRunner) copyRegisteredImports(registerName string, sourceName string) {
	fields := r.imports[sourceName]
	if len(fields) == 0 {
		return
	}
	if r.imports[registerName] == nil {
		r.imports[registerName] = map[string]wasmvm.Extern{}
	}
	maps.Copy(r.imports[registerName], fields)
}

// runInvoke invokes an exported function and requires the call to succeed.
func (r *wasmSpecWasmvmRunner) runInvoke(res *commandResult, cmd scriptCommand) {
	if _, err := r.invoke(cmd.action); err != nil {
		res.status = false
		res.detail = fmt.Sprintf("invoke failed: %v", err)
		return
	}
	res.status = true
}

// runAssertReturn invokes an action and compares its results with expected
// values.
func (r *wasmSpecWasmvmRunner) runAssertReturn(res *commandResult, cmd scriptCommand) {
	if cmd.getAction != nil {
		r.runAssertReturnGet(res, cmd)
		return
	}
	results, err := r.invoke(cmd.action)
	if err != nil {
		res.status = false
		res.detail = fmt.Sprintf("action failed: %v", err)
		return
	}

	if len(results) != len(cmd.expectValues) {
		res.status = false
		res.detail = fmt.Sprintf("result arity mismatch: got %d want %d", len(results), len(cmd.expectValues))
		return
	}
	for i := range results {
		want := cmd.expectValues[i]
		if !runtimeValueMatchesExpected(results[i], want) {
			res.status = false
			res.detail = fmt.Sprintf("result[%d] mismatch: got %s want %s", i, formatGotValueLikeExpected(results[i], want), formatExpectedValue(want))
			return
		}
	}
	res.status = true
}

// runAssertReturnGet reads an exported global and compares it with the expected
// value.
func (r *wasmSpecWasmvmRunner) runAssertReturnGet(res *commandResult, cmd scriptCommand) {
	results, err := r.get(cmd.getAction)
	if err != nil {
		res.status = false
		res.detail = fmt.Sprintf("get action failed: %v", err)
		return
	}
	if len(results) != len(cmd.expectValues) {
		res.status = false
		res.detail = fmt.Sprintf("result arity mismatch: got %d want %d", len(results), len(cmd.expectValues))
		return
	}
	for i := range results {
		want := cmd.expectValues[i]
		if !runtimeValueMatchesExpected(results[i], want) {
			res.status = false
			res.detail = fmt.Sprintf("result[%d] mismatch: got %s want %s", i, formatGotValueLikeExpected(results[i], want), formatExpectedValue(want))
			return
		}
	}
	res.status = true
}

// runAssertTrap requires an invocation or start-time instantiation to trap.
func (r *wasmSpecWasmvmRunner) runAssertTrap(res *commandResult, cmd scriptCommand) {
	var err error
	if cmd.moduleExpr != nil {
		err = r.instantiateTrapModule(cmd)
	} else {
		_, err = r.invoke(cmd.action)
	}
	if err == nil {
		res.status = false
		res.detail = "expected trap, got success"
		return
	}
	if cmd.expectText != "" && !matchesExpectedFailureText(err.Error(), cmd.expectText) {
		res.status = false
		res.detail = fmt.Sprintf("trap text mismatch: got %q want substring %q", err.Error(), cmd.expectText)
		return
	}
	res.status = true
}

// runAssertExhaustion requires an invocation to fail with an exhaustion-style
// error.
func (r *wasmSpecWasmvmRunner) runAssertExhaustion(res *commandResult, cmd scriptCommand) {
	_, err := r.invoke(cmd.action)
	if err == nil {
		res.status = false
		res.detail = "expected exhaustion, got success"
		return
	}
	if cmd.expectText != "" && !matchesExpectedFailureText(err.Error(), cmd.expectText) {
		res.status = false
		res.detail = fmt.Sprintf("exhaustion text mismatch: got %q want substring %q", err.Error(), cmd.expectText)
		return
	}
	res.status = true
}

// runAssertException requires an invocation to fail by throwing a wasm
// exception rather than trapping for another reason.
func (r *wasmSpecWasmvmRunner) runAssertException(res *commandResult, cmd scriptCommand) {
	_, err := r.invoke(cmd.action)
	if err == nil {
		res.status = false
		res.detail = "expected exception, got success"
		return
	}
	if !isWasmExceptionError(err) {
		res.status = false
		res.detail = fmt.Sprintf("expected exception, got %q", err.Error())
		return
	}
	res.status = true
}

// runAssertUnlinkable requires module compilation or instantiation to fail.
func (r *wasmSpecWasmvmRunner) runAssertUnlinkable(res *commandResult, cmd scriptCommand, opts runOptions) {
	m, _, err := compileWasmSpecCommandForWasmvm(cmd)
	if err == nil {
		_, err = r.instantiate(m)
	}
	if err == nil {
		res.status = false
		res.detail = "expected unlinkable module error, got success"
		return
	}
	if opts.strictErrorText && cmd.expectText != "" && !strings.Contains(err.Error(), cmd.expectText) {
		res.status = false
		res.detail = fmt.Sprintf("unlinkable error text mismatch: got %q want substring %q", err.Error(), cmd.expectText)
		return
	}
	res.status = true
}

// runAssertInvalid requires module validation to fail.
func (r *wasmSpecWasmvmRunner) runAssertInvalid(res *commandResult, cmd scriptCommand, opts runOptions) {
	err := compileWasmSpecInvalidModule(cmd)
	if err == nil {
		res.status = false
		res.detail = "expected invalid module error, got success"
		return
	}
	if opts.strictErrorText && cmd.expectText != "" && !strings.Contains(err.Error(), cmd.expectText) {
		res.status = false
		res.detail = fmt.Sprintf("invalid error text mismatch: got %q want substring %q", err.Error(), cmd.expectText)
		return
	}
	res.status = true
}

// runAssertMalformed requires module text or binary decoding to fail.
func (r *wasmSpecWasmvmRunner) runAssertMalformed(res *commandResult, cmd scriptCommand, opts runOptions) {
	err := compileWasmSpecMalformedModule(cmd)
	if err == nil {
		res.status = false
		res.detail = "expected malformed module error, got success"
		return
	}
	if opts.strictErrorText && cmd.expectText != "" && !malformedErrorMatches(err.Error(), cmd.expectText) {
		res.status = false
		res.detail = fmt.Sprintf("malformed error text mismatch: got %q want substring %q", err.Error(), cmd.expectText)
		return
	}
	res.status = true
}

// instantiateTrapModule compiles cmd's module expression and returns the
// instantiation error expected by an assert_trap command.
func (r *wasmSpecWasmvmRunner) instantiateTrapModule(cmd scriptCommand) error {
	m, _, err := compileWasmSpecCommandForWasmvm(cmd)
	if err != nil {
		return err
	}
	_, err = r.instantiate(m)
	return err
}

// instantiate instantiates m with the spec harness imports available to
// wasmvm-backed scripts.
func (r *wasmSpecWasmvmRunner) instantiate(m *wasmir.Module) (*wasmvm.ModuleInstance, error) {
	return r.rt.Instantiate(m, r.imports)
}

// get reads one exported global through wasmvm and converts it to the wasmspec
// harness value representation.
func (r *wasmSpecWasmvmRunner) get(action *getAction) ([]runtimeValue, error) {
	if action == nil {
		return nil, fmt.Errorf("nil get action")
	}
	if synth, ok := r.lookupSyntheticModule(action.moduleName); ok {
		return synth.get(action.globalName)
	}
	inst, meta, err := r.lookupInstance(action.moduleName)
	if err != nil {
		return nil, err
	}
	if _, ok := meta.globalExports[action.globalName]; !ok {
		return nil, fmt.Errorf("exported global %q not found", action.globalName)
	}
	g, ok := inst.ExportedGlobal(action.globalName)
	if !ok {
		return nil, fmt.Errorf("exported global %q not found in wasmvm instance", action.globalName)
	}
	value, err := g.Value()
	if err != nil {
		return nil, err
	}
	converted, err := wasmvmValueToRuntimeValue(value)
	if err != nil {
		return nil, err
	}
	return []runtimeValue{converted}, nil
}

// invoke calls one exported function through wasmvm and converts results to
// the wasmspec harness value representation.
func (r *wasmSpecWasmvmRunner) invoke(action *invokeAction) ([]runtimeValue, error) {
	if action == nil {
		return nil, fmt.Errorf("nil invoke action")
	}
	if synth, ok := r.lookupSyntheticModule(action.moduleName); ok {
		return synth.invoke(action.funcName)
	}
	inst, meta, err := r.lookupInstance(action.moduleName)
	if err != nil {
		return nil, err
	}
	sig, ok := meta.funcExports[action.funcName]
	if !ok {
		return nil, fmt.Errorf("exported function %q not found", action.funcName)
	}
	if len(sig.Params) != len(action.args) {
		return nil, fmt.Errorf("invoke arg arity mismatch for %q: got %d want %d", action.funcName, len(action.args), len(sig.Params))
	}
	f, ok := inst.ExportedFunc(action.funcName)
	if !ok {
		return nil, fmt.Errorf("exported function %q not found in wasmvm instance", action.funcName)
	}
	args := make([]wasmvm.Value, len(action.args))
	for i, arg := range action.args {
		args[i], err = scriptValueToWasmvmValue(arg, sig.Params[i])
		if err != nil {
			return nil, fmt.Errorf("arg[%d]: %w", i, err)
		}
	}
	results, err := f.Call(args...)
	if err != nil {
		return nil, err
	}
	return wasmvmValuesToRuntimeValues(results)
}

// buildSyntheticConsumerModule delegates recognized resource-identity consumer
// modules to the shared wasmspec synthetic harness path.
func (r *wasmSpecWasmvmRunner) buildSyntheticConsumerModule(moduleExpr *textformat.SExpr) (*syntheticModule, bool, error) {
	return buildSyntheticConsumerModule(moduleExpr, r.lookupSyntheticInstance)
}

// lookupSyntheticInstance resolves a script module name to a synthetic
// resource-identity instance.
func (r *wasmSpecWasmvmRunner) lookupSyntheticInstance(name string) (*syntheticInstance, bool) {
	if aliased, ok := r.moduleAlias[name]; ok {
		name = aliased
	}
	inst, ok := r.syntheticInstances[name]
	return inst, ok
}

// lookupSyntheticModule resolves a script module name to a synthetic module
// implemented by the harness.
func (r *wasmSpecWasmvmRunner) lookupSyntheticModule(scriptModuleName string) (*syntheticModule, bool) {
	if scriptModuleName == "" {
		if r.currentSynthetic != nil {
			return r.currentSynthetic, true
		}
		return nil, false
	}
	runtimeName := scriptModuleName
	if aliased, ok := r.moduleAlias[scriptModuleName]; ok {
		runtimeName = aliased
	}
	synth, ok := r.syntheticModules[runtimeName]
	return synth, ok
}

// lookupInstance resolves a script module name to a wasmvm instance and its
// decoded export metadata.
func (r *wasmSpecWasmvmRunner) lookupInstance(scriptModuleName string) (*wasmvm.ModuleInstance, *moduleMetadata, error) {
	if scriptModuleName == "" {
		if r.current == nil || r.currentMeta == nil {
			return nil, nil, fmt.Errorf("no current module")
		}
		return r.current, r.currentMeta, nil
	}
	runtimeName := scriptModuleName
	if aliased, ok := r.moduleAlias[scriptModuleName]; ok {
		runtimeName = aliased
	}
	inst, ok := r.instances[runtimeName]
	if !ok {
		return nil, nil, fmt.Errorf("module %q not found", scriptModuleName)
	}
	meta, ok := r.moduleMeta[runtimeName]
	if !ok || meta == nil {
		return nil, nil, fmt.Errorf("module metadata for %q not found", runtimeName)
	}
	return inst, meta, nil
}

// compileWasmSpecCommandForWasmvm compiles a module-bearing command into a
// validated module and decoded export metadata.
func compileWasmSpecCommandForWasmvm(cmd scriptCommand) (*wasmir.Module, *moduleMetadata, error) {
	wasmBytes, ok, err := compileWasmSpecCommandForPrintRoundTrip(cmd)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return nil, nil, fmt.Errorf("command %s does not contain a wasm module", cmd.kind)
	}
	m, err := watgo.DecodeWASM(wasmBytes)
	if err != nil {
		return nil, nil, err
	}
	if err := watgo.ValidateModule(m); err != nil {
		return nil, nil, err
	}
	meta, err := decodeModuleMetadata(wasmBytes)
	if err != nil {
		return nil, nil, err
	}
	return m, meta, nil
}

// compileWasmSpecInvalidModule compiles cmd as an invalid-module assertion and
// returns the validation error, if any.
func compileWasmSpecInvalidModule(cmd scriptCommand) error {
	if isModuleBinaryExpr(cmd.moduleExpr) {
		wasmBytes, err := parseBinaryModuleBytes(cmd.moduleExpr)
		if err != nil {
			return err
		}
		m, err := binaryformat.DecodeModule(wasmBytes)
		if err != nil {
			return err
		}
		return validate.ValidateModule(m, nil)
	}
	_, _, err := compileWasmSpecModuleTextFromCommand(cmd)
	return err
}

// compileWasmSpecMalformedModule compiles cmd as a malformed-module assertion
// and returns the text or binary decoding error, if any.
func compileWasmSpecMalformedModule(cmd scriptCommand) error {
	if isModuleBinaryExpr(cmd.moduleExpr) {
		wasmBytes, err := parseBinaryModuleBytes(cmd.moduleExpr)
		if err != nil {
			return err
		}
		_, err = binaryformat.DecodeModule(wasmBytes)
		return err
	}
	_, err := compileWATModule(cmd.quotedWAT)
	return err
}

// compileWasmSpecModuleTextFromCommand compiles the text module carried by cmd.
func compileWasmSpecModuleTextFromCommand(cmd scriptCommand) ([]byte, bool, error) {
	if cmd.quotedWAT != "" {
		return compileWasmSpecModuleText(cmd.quotedWAT)
	}
	src, err := sexprToWAT(cmd.moduleExpr)
	if err != nil {
		return nil, false, fmt.Errorf("module text generation failed: %w", err)
	}
	return compileWasmSpecModuleText(src)
}

// scriptValueToWasmvmValue converts a parsed spec-script constant argument to
// a wasmvm runtime value of targetType.
func scriptValueToWasmvmValue(arg scriptValue, targetType wasmir.ValueType) (wasmvm.Value, error) {
	switch arg.kind {
	case valueI32Const:
		return wasmvm.I32(int32(uint32(arg.bits))), nil
	case valueI64Const:
		return wasmvm.I64(int64(arg.bits)), nil
	case valueF32Const:
		return wasmvm.F32(math.Float32frombits(uint32(arg.bits))), nil
	case valueF64Const:
		return wasmvm.F64(math.Float64frombits(arg.bits)), nil
	case valueF32NaNCanonical, valueF32NaNArithmetic:
		return wasmvm.F32(math.Float32frombits(0x7fc00000)), nil
	case valueF64NaNCanonical, valueF64NaNArithmetic:
		return wasmvm.F64(math.Float64frombits(0x7ff8000000000000)), nil
	case valueV128Const:
		return wasmvm.Value{Type: wasmir.ValueTypeV128, V128: arg.v128}, nil
	case valueRefNull:
		return wasmvm.Value{Type: targetType}, nil
	case valueRefExtern:
		value := wasmvm.ExternRef(arg.bits)
		value.Type = targetType
		return value, nil
	case valueRefHost:
		value := wasmvm.ExternRef(arg.bits)
		value.Type = targetType
		return value, nil
	default:
		return wasmvm.Value{}, fmt.Errorf("unsupported invoke arg kind %q", arg.kind)
	}
}

// wasmvmValuesToRuntimeValues converts wasmvm results to the wasmspec harness
// value representation.
func wasmvmValuesToRuntimeValues(values []wasmvm.Value) ([]runtimeValue, error) {
	out := make([]runtimeValue, len(values))
	for i, value := range values {
		converted, err := wasmvmValueToRuntimeValue(value)
		if err != nil {
			return nil, fmt.Errorf("result[%d]: %w", i, err)
		}
		out[i] = converted
	}
	return out, nil
}

// wasmvmValueToRuntimeValue converts one wasmvm result to the wasmspec harness
// value representation.
func wasmvmValueToRuntimeValue(value wasmvm.Value) (runtimeValue, error) {
	switch value.Type.Kind {
	case wasmir.ValueKindI32:
		return runtimeValue{scalar: uint64(uint32(value.I32))}, nil
	case wasmir.ValueKindI64:
		return runtimeValue{scalar: uint64(value.I64)}, nil
	case wasmir.ValueKindF32:
		return runtimeValue{scalar: uint64(math.Float32bits(value.F32))}, nil
	case wasmir.ValueKindF64:
		return runtimeValue{scalar: math.Float64bits(value.F64)}, nil
	case wasmir.ValueKindV128:
		return runtimeValue{v128: value.V128, isV128: true}, nil
	case wasmir.ValueKindRef:
		if value.Ref.Kind == 0 {
			return runtimeValue{}, nil
		}
		if value.Ref.Kind == wasmvm.RefKindExtern && value.Type.HeapType.Kind == wasmir.HeapKindExtern {
			return runtimeValue{scalar: encodedRefExternTag | value.Ref.ExternID}, nil
		}
		if value.Ref.Kind == wasmvm.RefKindExtern {
			return runtimeValue{scalar: encodedRefHostTag | value.Ref.ExternID}, nil
		}
		switch value.Ref.Kind {
		case wasmvm.RefKindI31:
			return runtimeValue{scalar: encodedRefI31}, nil
		case wasmvm.RefKindStruct:
			return runtimeValue{scalar: encodedRefStruct}, nil
		case wasmvm.RefKindArray:
			return runtimeValue{scalar: encodedRefArray}, nil
		default:
			return runtimeValue{scalar: encodedRefEq}, nil
		}
	default:
		return runtimeValue{}, fmt.Errorf("unsupported wasmvm value type %s", value.Type)
	}
}
