// Package wasmvm exposes watgo's minimal WebAssembly interpreter runtime.
package wasmvm

import (
	"github.com/eliben/watgo/internal/vm"
	"github.com/eliben/watgo/wasmir"
)

// Value is one runtime WebAssembly value.
type Value = vm.Value

// Imports maps import module names and field names to runtime externs.
type Imports = vm.Imports

// Extern is one runtime object supplied for a module import.
type Extern = vm.Extern

// HostFunc is a Go function exposed as a WebAssembly function import.
type HostFunc = vm.HostFunc

// Context is passed to host functions.
type Context = vm.Context

// Runtime owns instantiated modules and host/runtime state.
type Runtime = vm.Runtime

// ModuleInstance is one instantiated WebAssembly module.
type ModuleInstance = vm.ModuleInstance

// Func is a callable WebAssembly or host function.
type Func = vm.Func

// NewRuntime constructs an empty runtime.
func NewRuntime() *Runtime {
	return vm.NewRuntime()
}

// NewHostFunc constructs a function import from a Go callback.
func NewHostFunc(params, results []wasmir.ValueType, fn func(ctx *Context, args []Value) ([]Value, error)) HostFunc {
	return vm.NewHostFunc(params, results, fn)
}

// I32 constructs an i32 runtime value.
func I32(v int32) Value {
	return vm.I32(v)
}

// I64 constructs an i64 runtime value.
func I64(v int64) Value {
	return vm.I64(v)
}

// F32 constructs an f32 runtime value.
func F32(v float32) Value {
	return vm.F32(v)
}

// F64 constructs an f64 runtime value.
func F64(v float64) Value {
	return vm.F64(v)
}
