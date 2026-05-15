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
	hosts, err := buildHostFuncs(m, imports)
	if err != nil {
		return nil, err
	}

	inst := &ModuleInstance{
		rt:      rt,
		hosts:   hosts,
		exports: make(map[string]*Func),
	}
	vmInst, err := vm.Instantiate(m, vmResolver{inst: inst})
	if err != nil {
		return nil, err
	}
	inst.vm = vmInst

	for _, exp := range m.Exports {
		if exp.Kind != wasmir.ExternalKindFunction {
			continue
		}
		if _, err := inst.vm.FuncType(exp.Index); err != nil {
			return nil, fmt.Errorf("export %q: function index %d out of range", exp.Name, exp.Index)
		}
		inst.exports[exp.Name] = &Func{inst: inst, index: exp.Index}
	}
	return inst, nil
}

// ModuleInstance is one instantiated WebAssembly module.
//
// A ModuleInstance owns the public binding to the internal VM instance and its
// exported functions. Values returned by ExportedFunc are bound to this
// instance.
type ModuleInstance struct {
	rt      *Runtime
	vm      *vm.Instance
	hosts   []HostFunc
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
	return f.inst.vm.CallFunc(f.index, args)
}

// buildHostFuncs resolves and type-checks imported host functions in function
// index order.
func buildHostFuncs(m *wasmir.Module, imports Imports) ([]HostFunc, error) {
	var hosts []HostFunc
	for _, imp := range m.Imports {
		if imp.Kind != wasmir.ExternalKindFunction {
			return nil, fmt.Errorf("unsupported import %q.%q kind %d", imp.Module, imp.Name, imp.Kind)
		}
		host, err := resolveHostFunc(imports, imp)
		if err != nil {
			return nil, err
		}
		if err := checkHostFuncType(m, imp, host); err != nil {
			return nil, err
		}
		hosts = append(hosts, host)
	}
	return hosts, nil
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
func checkHostFuncType(m *wasmir.Module, imp wasmir.Import, host HostFunc) error {
	if int(imp.TypeIdx) >= len(m.Types) || m.Types[imp.TypeIdx].Kind != wasmir.TypeDefKindFunc {
		return fmt.Errorf("import %q.%q has invalid function type", imp.Module, imp.Name)
	}
	ft := m.Types[imp.TypeIdx]
	if !slices.Equal(host.Params, ft.Params) || !slices.Equal(host.Results, ft.Results) {
		return fmt.Errorf("import %q.%q type mismatch", imp.Module, imp.Name)
	}
	if host.Func == nil {
		return fmt.Errorf("import %q.%q has nil function", imp.Module, imp.Name)
	}
	return nil
}

type vmResolver struct {
	inst *ModuleInstance
}

// CallFunc invokes an imported host function at index.
func (r vmResolver) CallFunc(index uint32, args []vm.Value) ([]vm.Value, error) {
	if int(index) >= len(r.inst.hosts) {
		return nil, fmt.Errorf("host function index %d out of range", index)
	}
	return r.inst.hosts[index].Func(&Context{Runtime: r.inst.rt, Instance: r.inst}, args)
}
