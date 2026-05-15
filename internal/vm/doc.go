// Package vm contains the private execution engine used by wasmvm.
//
// wasmvm owns the public API, imports, exports, and host callbacks.
//
// vm owns the executable instance state: lowered functions, globals, memories,
// tables, data segments, element segments, and instruction semantics.
//
// Instantiate builds an Instance from a validated wasmir.Module.
// Instance.CallFunc dispatches function-index calls, and Instance.FuncType
// exposes signatures needed by wasmvm. Resolver is the only bridge back to
// wasmvm; it is used only for imported function calls.
//
// A wasm-to-wasm call re-enters Instance.CallFunc and creates another executor
// frame. A wasm-to-host call goes through Resolver.CallFunc:
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
