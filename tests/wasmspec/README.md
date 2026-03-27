# Wasm Spec Harness

This directory contains the integration harness for running `.wast` scripts from
the WebAssembly spec tests against `watgo`.

Detailed per-command/debug tracing is disabled by default. To enable it for a
run, set `WATGO_WASMSPEC_DEBUG=1`.

## Files

- [scripts/](./scripts)
  The actual `.wast` script corpus.
- [wasmspec_test.go](./wasmspec_test.go)
  Discovers scripts under `scripts/`, runs each one as a Go subtest, and
  reports per-command failures.
- [wasmspec_harness.go](./wasmspec_harness.go)
  The main harness implementation.
- [node_wasm_runner.js](./node_wasm_runner.js)
  A small JSON-over-stdio bridge to Node's `WebAssembly` API.

## High-Level Flow

For each `.wast` file:

1. [wasmspec_test.go](./wasmspec_test.go) reads the script and parses it into
   top-level commands such as `module`, `invoke`, `assert_return`,
   `assert_trap`, and so on.
2. [wasmspec_harness.go](./wasmspec_harness.go) executes those commands in
   order with a `scriptRunner`.
3. Text modules are compiled by `watgo`; binary modules are decoded directly.
4. Modules are instantiated and invoked through
   [node_wasm_runner.js](./node_wasm_runner.js).
5. The harness compares results and trap text against the script's expected
   assertions.

The important point is that `.wast` execution is stateful: later commands can
refer to modules instantiated earlier in the same script, use `(register ...)`,
and observe mutated memory/table/global state.

## Usage of the Node runtime

`watgo` currently compiles/validates/modules, but it is not a runtime. The
harness therefore uses Node as the execution engine for instantiated wasm
modules.

[node_wasm_runner.js](./node_wasm_runner.js) keeps one Node process alive for
the duration of a single `.wast` file. The Go harness sends line-delimited JSON
requests like:

- `instantiate`
- `validate`
- `invoke`
- `get`

and receives JSON responses back.

This bridge is intentionally narrow: it only supports the value kinds and
operations needed by the current spec tests.

Some wasm results are awkward to observe exactly through the JS embedding API:

- `f32`/`f64` results can lose exact NaN payload information when converted to
  JS numbers.
- `v128` results are not directly exposed as raw SIMD bytes in a useful form.
- `anyref` sometimes needs in-wasm classification with `ref.test`.

To handle this, [node_wasm_runner.js](./node_wasm_runner.js) sometimes builds
small helper wasm modules on the fly. These wrappers import the target function,
call it inside wasm, and convert the result into a JS-friendly exact form such
as integer bits or raw bytes before it crosses back into JS.
