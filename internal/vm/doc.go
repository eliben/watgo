// Package vm contains the private execution engine used by wasmvm.
//
// The public wasmvm package owns the user-facing API: imports, exports, host
// callbacks, and the public function-call surface. This package owns the
// execution state that is opaque to host code: compiled module-defined
// functions, globals, memories, tables, data segments, element segments, and
// instruction semantics.
//
// The package API is intentionally narrow:
//
//   - Value is the runtime value representation. wasmvm re-exports it as
//     wasmvm.Value.
//   - Instantiate builds an Instance from a validated wasmir.Module.
//   - Instance owns the module's executable state.
//   - Instance.CallFunc dispatches a function-index call, whether the callee is
//     module-defined or imported.
//   - Instance.FuncType exposes function signatures so wasmvm can validate
//     function exports and public calls.
//   - Resolver is the only bridge back to wasmvm. It is used when Instance
//     dispatches an imported function index; globals, memories, tables, data
//     segments, element segments, and lowered instructions all stay inside
//     this package.
//
// Function compilation is deliberately separate from wasmir. wasmir is the
// semantic representation of a module, but it is not optimized for repeated
// execution. During Instantiate, each module-defined function is lowered once
// into a linear instruction stream with execution-focused immediates, including
// precomputed structured-control targets. Calls then reuse that lowered form.
//
// The life of a simple exported function call is:
//
//   - wasmvm.Func.Call passes the exported function index to Instance.CallFunc.
//   - Instance.CallFunc checks the callee signature. Imported function indices
//     are sent to Resolver.CallFunc; module-defined function indices enter a
//     new executor frame.
//   - The executor runs the lowered instruction stream with its own operand
//     stack and locals, reading and writing Instance-owned globals, memories,
//     tables, data segments, and element segments directly.
//   - When the executor reaches a wasm call instruction, it pops the callee's
//     arguments and re-enters Instance.CallFunc with the target function index.
//   - Results return back through the same chain of Instance.CallFunc and
//     executor frames.
//
// For example, if exported wasm function A calls wasm function B, and B calls a
// host import, the control path is:
//
//	wasmvm.Func.Call(A)
//	  -> Instance.CallFunc(A)
//	  -> executeFunction(A)
//	  -> Instance.CallFunc(B)
//	  -> executeFunction(B)
//	  -> Instance.CallFunc(import)
//	  -> Resolver.CallFunc(import)
//	  -> wasmvm HostFunc callback
package vm
