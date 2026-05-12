// Package wasmvm exposes a minimal WebAssembly interpreter runtime for wasmir
// modules.
//
// The package can instantiate an already-validated wasmir.Module, look up
// exported functions, call them with runtime values, and satisfy WebAssembly
// function imports with Go callbacks.
package wasmvm

import (
	"fmt"
	"slices"

	"github.com/eliben/watgo/internal/vm"
	"github.com/eliben/watgo/wasmir"
)

// Value is one runtime WebAssembly value passed into or returned from a Func.
//
// Type identifies which payload field is meaningful. For example, values with
// Type set to wasmir.ValueTypeI32 use I32, while values with Type set to
// wasmir.ValueTypeF64 use F64. Prefer the I32, I64, F32, and F64 constructors
// over constructing Value directly.
type Value = vm.Value

// I32 returns a runtime Value whose type is wasmir.ValueTypeI32 and whose
// payload is v.
func I32(v int32) Value {
	return Value{Type: wasmir.ValueTypeI32, I32: v}
}

// I64 returns a runtime Value whose type is wasmir.ValueTypeI64 and whose
// payload is v.
func I64(v int64) Value {
	return Value{Type: wasmir.ValueTypeI64, I64: v}
}

// F32 returns a runtime Value whose type is wasmir.ValueTypeF32 and whose
// payload is v.
func F32(v float32) Value {
	return Value{Type: wasmir.ValueTypeF32, F32: v}
}

// F64 returns a runtime Value whose type is wasmir.ValueTypeF64 and whose
// payload is v.
func F64(v float64) Value {
	return Value{Type: wasmir.ValueTypeF64, F64: v}
}

// Imports maps WebAssembly import module names and field names to host externs.
//
// For an import such as (import "env" "inc" (func ...)), the corresponding
// Go value belongs at imports["env"]["inc"]. Only function imports are
// supported for now, so the extern value should be a HostFunc created with
// NewHostFunc.
type Imports map[string]map[string]Extern

// Extern is a runtime object supplied for a module import.
//
// HostFunc is currently the only supported Extern implementation. This
// interface is present so Imports can grow to memory, table, or global imports
// later without changing its shape.
type Extern interface {
	isExtern()
}

// HostFunc is a Go callback exposed as a WebAssembly function import.
//
// Params and Results are the WebAssembly function signature expected by the
// importing module. Func receives the calling context and argument values in
// parameter order, and returns result values in result order. The runtime checks
// the argument and result counts and value types against Params and Results.
type HostFunc struct {
	// Params is the host function's WebAssembly parameter type list.
	Params []wasmir.ValueType

	// Results is the host function's WebAssembly result type list.
	Results []wasmir.ValueType

	// Func is called when WebAssembly code invokes this host function.
	//
	// args contains one Value per parameter. The returned slice must contain one
	// Value per result. Returning an error aborts the WebAssembly call and
	// propagates the error to Func.Call.
	Func func(ctx *Context, args []Value) ([]Value, error)
}

// isExtern marks HostFunc as a valid import object.
func (HostFunc) isExtern() {}

// NewHostFunc returns a HostFunc with the given WebAssembly signature and Go
// callback.
//
// params and results are copied by reference, so callers should treat them as
// immutable after passing them here. fn must be non-nil before the HostFunc is
// used to instantiate a module; otherwise Instantiate returns an error.
func NewHostFunc(params, results []wasmir.ValueType, fn func(ctx *Context, args []Value) ([]Value, error)) HostFunc {
	return HostFunc{Params: params, Results: results, Func: fn}
}

// Context is passed to host functions during a WebAssembly call.
//
// Runtime is the runtime that owns the current instance. Instance is the module
// instance that made the call. These fields let host functions inspect or call
// back into the instance as the API grows.
type Context struct {
	// Runtime owns Instance and the current call.
	Runtime *Runtime

	// Instance is the WebAssembly module instance that invoked the host function.
	Instance *ModuleInstance
}

// Runtime owns instantiated modules and runtime-wide state.
//
// A Runtime is created with NewRuntime.
type Runtime struct{}

// NewRuntime returns a new empty Runtime.
func NewRuntime() *Runtime {
	return &Runtime{}
}

// Instantiate instantiates m with the supplied imports.
//
// m must already be validated before it is passed to Instantiate. In
// particular, modules produced from WAT should be validated using the hints
// produced by WAT parsing before reaching this runtime API.
//
// imports supplies host functions needed by m's import section; pass nil when
// the module has no imports. On success, Instantiate returns a ModuleInstance
// whose exported functions can be obtained with ModuleInstance.ExportedFunc. It
// returns an error when an import is missing, an import has the wrong type, or
// the module uses an import/export/instruction kind this minimal runtime does
// not support yet.
func (rt *Runtime) Instantiate(m *wasmir.Module, imports Imports) (*ModuleInstance, error) {
	if m == nil {
		return nil, fmt.Errorf("module is nil")
	}

	inst := &ModuleInstance{
		rt:      rt,
		m:       m,
		exports: make(map[string]*Func),
	}
	if err := inst.buildGlobals(); err != nil {
		return nil, err
	}
	if err := inst.buildFuncs(imports); err != nil {
		return nil, err
	}
	for _, exp := range m.Exports {
		if exp.Kind != wasmir.ExternalKindFunction {
			continue
		}
		if int(exp.Index) >= len(inst.funcs) {
			return nil, fmt.Errorf("export %q: function index %d out of range", exp.Name, exp.Index)
		}
		inst.exports[exp.Name] = &Func{inst: inst, index: exp.Index}
	}
	return inst, nil
}

// ModuleInstance is one instantiated WebAssembly module.
//
// A ModuleInstance owns the module's function index space and exported
// functions. Values returned by ExportedFunc are bound to this instance.
type ModuleInstance struct {
	rt      *Runtime
	m       *wasmir.Module
	funcs   []funcInst
	globals []globalInst
	exports map[string]*Func
}

// ExportedFunc returns the exported function with the given name.
//
// The returned boolean is false when name is not exported as a function. Other
// export kinds are ignored by this method. The returned Func is bound to this
// ModuleInstance and can be invoked with Func.Call.
func (inst *ModuleInstance) ExportedFunc(name string) (*Func, bool) {
	f, ok := inst.exports[name]
	return f, ok
}

// Func is a callable WebAssembly function exported from a ModuleInstance.
//
// A Func is obtained with ModuleInstance.ExportedFunc. Calls validate argument
// count and value types against the function's WebAssembly signature.
type Func struct {
	inst  *ModuleInstance
	index uint32
}

// Call invokes f with WebAssembly runtime values.
//
// args must contain one Value per function parameter, in parameter order. On
// success, Call returns one Value per function result, in result order. It
// returns an error when the argument count or types are wrong, when a host
// callback returns an error, or when execution traps in the currently supported
// instruction subset.
func (f *Func) Call(args ...Value) ([]Value, error) {
	return f.inst.callFunc(f.index, args)
}

type funcInst struct {
	// typeIdx indexes inst.m.Types and describes both host and module-defined
	// functions in the unified function index space.
	typeIdx uint32

	// host is non-nil for imported host functions. Such functions are executed
	// directly by callFunc rather than through internal/vm.
	host *HostFunc

	// code is non-nil for module-defined functions. It is compiled once during
	// instantiation from wasmir.Function into internal/vm's execution form.
	code *vm.Function
}

type globalInst struct {
	typ     wasmir.ValueType
	mutable bool
	value   Value
}

// buildGlobals creates the instance global address space.
func (inst *ModuleInstance) buildGlobals() error {
	for i, g := range inst.m.Globals {
		if g.ImportModule != "" || g.ImportName != "" {
			return fmt.Errorf("unsupported global import %q.%q", g.ImportModule, g.ImportName)
		}
		value, err := vm.EvalConstExpr(g.Init, vmResolver{inst: inst, globalInit: true})
		if err != nil {
			return fmt.Errorf("global[%d]: %w", i, err)
		}
		if value.Type != g.Type {
			return fmt.Errorf("global[%d]: initializer type mismatch: got %s want %s", i, value.Type, g.Type)
		}
		inst.globals = append(inst.globals, globalInst{typ: g.Type, mutable: g.Mutable, value: value})
	}
	return nil
}

// buildFuncs creates the instance function address space.
//
// WebAssembly numbers imported functions before module-defined functions, so
// the order here has to match the function index space used by exports and
// call instructions.
func (inst *ModuleInstance) buildFuncs(imports Imports) error {
	for _, imp := range inst.m.Imports {
		if imp.Kind != wasmir.ExternalKindFunction {
			return fmt.Errorf("unsupported import %q.%q kind %d", imp.Module, imp.Name, imp.Kind)
		}
		host, err := resolveHostFunc(imports, imp)
		if err != nil {
			return err
		}
		if err := inst.checkHostFuncType(imp, host); err != nil {
			return err
		}
		inst.funcs = append(inst.funcs, funcInst{typeIdx: imp.TypeIdx, host: &host})
	}
	for i := range inst.m.Funcs {
		f := &inst.m.Funcs[i]
		code, err := vm.CompileFunction(f)
		if err != nil {
			return fmt.Errorf("func[%d]: %w", len(inst.funcs), err)
		}
		inst.funcs = append(inst.funcs, funcInst{typeIdx: f.TypeIdx, code: code})
	}
	return nil
}

// resolveHostFunc finds the Go callback supplied for a function import.
func resolveHostFunc(imports Imports, imp wasmir.Import) (HostFunc, error) {
	fields, ok := imports[imp.Module]
	if !ok {
		return HostFunc{}, fmt.Errorf("missing import module %q", imp.Module)
	}
	ext, ok := fields[imp.Name]
	if !ok {
		return HostFunc{}, fmt.Errorf("missing import %q.%q", imp.Module, imp.Name)
	}
	switch host := ext.(type) {
	case HostFunc:
		return host, nil
	case *HostFunc:
		if host == nil {
			return HostFunc{}, fmt.Errorf("import %q.%q is nil", imp.Module, imp.Name)
		}
		return *host, nil
	default:
		return HostFunc{}, fmt.Errorf("import %q.%q is not a function", imp.Module, imp.Name)
	}
}

// checkHostFuncType checks that a supplied host function matches the module's
// declared import type.
func (inst *ModuleInstance) checkHostFuncType(imp wasmir.Import, host HostFunc) error {
	if int(imp.TypeIdx) >= len(inst.m.Types) || inst.m.Types[imp.TypeIdx].Kind != wasmir.TypeDefKindFunc {
		return fmt.Errorf("import %q.%q has invalid function type", imp.Module, imp.Name)
	}
	ft := inst.m.Types[imp.TypeIdx]
	if !slices.Equal(host.Params, ft.Params) || !slices.Equal(host.Results, ft.Results) {
		return fmt.Errorf("import %q.%q type mismatch", imp.Module, imp.Name)
	}
	if host.Func == nil {
		return fmt.Errorf("import %q.%q has nil function", imp.Module, imp.Name)
	}
	return nil
}

// callFunc dispatches a function-index call.
func (inst *ModuleInstance) callFunc(index uint32, args []Value) ([]Value, error) {
	if int(index) >= len(inst.funcs) {
		return nil, fmt.Errorf("function index %d out of range", index)
	}
	fn := inst.funcs[index]
	ft, err := inst.funcType(fn.typeIdx)
	if err != nil {
		return nil, err
	}
	if err := vm.CheckArgs(ft.Params, args); err != nil {
		return nil, fmt.Errorf("func[%d]: %w", index, err)
	}
	if fn.host != nil {
		results, err := fn.host.Func(&Context{Runtime: inst.rt, Instance: inst}, args)
		if err != nil {
			return nil, err
		}
		if err := vm.CheckResults(ft.Results, results); err != nil {
			return nil, fmt.Errorf("func[%d]: %w", index, err)
		}
		return results, nil
	}
	return vm.ExecuteFunction(fn.code, ft, args, vmResolver{inst: inst})
}

// funcType returns the function type referenced by typeIdx.
func (inst *ModuleInstance) funcType(typeIdx uint32) (wasmir.TypeDef, error) {
	if int(typeIdx) >= len(inst.m.Types) || inst.m.Types[typeIdx].Kind != wasmir.TypeDefKindFunc {
		return wasmir.TypeDef{}, fmt.Errorf("type index %d is not a function type", typeIdx)
	}
	return inst.m.Types[typeIdx], nil
}

type vmResolver struct {
	inst *ModuleInstance

	// globalInit applies the stricter global.get rules used while evaluating
	// module-defined global initializer expressions.
	globalInit bool
}

func (r vmResolver) FuncType(index uint32) (wasmir.TypeDef, error) {
	inst := r.inst
	if int(index) >= len(inst.funcs) {
		return wasmir.TypeDef{}, fmt.Errorf("call function index %d out of range", index)
	}
	return inst.funcType(inst.funcs[index].typeIdx)
}

func (r vmResolver) CallFunc(index uint32, args []Value) ([]Value, error) {
	return r.inst.callFunc(index, args)
}

func (r vmResolver) GlobalGet(index uint32) (Value, error) {
	if int(index) >= len(r.inst.globals) {
		return Value{}, fmt.Errorf("global index %d out of range", index)
	}
	g := r.inst.globals[index]
	if r.globalInit && g.mutable {
		return Value{}, fmt.Errorf("global %d is mutable", index)
	}
	return g.value, nil
}

func (r vmResolver) GlobalSet(index uint32, value Value) error {
	if int(index) >= len(r.inst.globals) {
		return fmt.Errorf("global index %d out of range", index)
	}
	g := &r.inst.globals[index]
	if !g.mutable {
		return fmt.Errorf("global %d is immutable", index)
	}
	if value.Type != g.typ {
		return fmt.Errorf("global.set %d got %s, want %s", index, value.Type, g.typ)
	}
	g.value = value
	return nil
}
