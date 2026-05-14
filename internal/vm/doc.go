// Package vm contains the private execution representation used by wasmvm.
//
//   - The public wasmvm package owns the user-facing runtime: module
//     instantiation, imports, exports, host callbacks, and the WebAssembly
//     function index space.
//   - This package owns the runtime form of module-defined functions.
//     CompileFunction lowers wasmir.Function into Function, and ExecuteFunction
//     interprets compiled Function values.
//
// The package API is intentionally small:
//
//   - Value is the runtime value representation. wasmvm re-exports it as
//     wasmvm.Value.
//   - Function is an opaque compiled module-defined function.
//   - CompileFunction builds a Function from a wasmir.Function at instantiation
//     time.
//   - ExecuteFunction runs a Function when wasmvm dispatches to module-defined
//     code.
//   - EvalConstExpr evaluates lowered module-level constant expressions into
//     runtime Values during instantiation.
//   - Resolver is implemented by the package that owns the instantiated
//     module environment. ExecuteFunction and EvalConstExpr use it for
//     operations that may cross into host-visible state, such as function
//     calls, global access, memory access, and table access, without knowing
//     about module instances or host imports.
//   - CheckArgs and CheckResults are shared signature checks used at call
//     boundaries.
//
// Function compilation is deliberately separate from wasmir. wasmir is the
// semantic representation of a module, but it is not optimized for repeated
// execution. Function stores a linear instruction stream with execution-focused
// immediates, including precomputed structured-control targets. Each
// module-defined function is compiled once during wasmvm instantiation and then
// reused for every call.
//
// The life of a simple exported function call is:
//
//   - wasmvm.Func.Call enters wasmvm.ModuleInstance's function dispatcher with
//     a WebAssembly function index.
//   - The dispatcher checks the callee signature. Imported host functions are
//     invoked directly through their Go callback; module-defined functions are
//     passed to ExecuteFunction.
//   - ExecuteFunction runs the compiled instruction stream with its own operand
//     stack and locals, using Resolver when the instruction stream calls a
//     function or reads/writes instance globals, memories, or tables.
//   - When ExecuteFunction reaches a wasm call instruction, it pops the
//     callee's arguments, asks Resolver for the callee's signature, and invokes
//     Resolver.CallFunc.
//   - wasmvm's Resolver implementation re-enters the same function dispatcher,
//     so a wasm function calling another wasm function creates a
//     new ExecuteFunction frame, while a wasm function calling an import
//     reaches the host callback.
//   - Results return back through the same chain of dispatcher and
//     ExecuteFunction frames.
//
// For example, if exported wasm function A calls wasm function B, and B calls a
// host import, the control path is:
//
//	Func.Call(A)
//	  -> wasmvm dispatcher(A)
//	  -> ExecuteFunction(A)
//	  -> Resolver.CallFunc(B)
//	  -> wasmvm dispatcher(B)
//	  -> ExecuteFunction(B)
//	  -> Resolver.CallFunc(host)
//	  -> wasmvm dispatcher(host)
//	  -> HostFunc callback
package vm
