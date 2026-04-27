# watgo Notes

`watgo` is a Go toolkit for parsing, printing, validating, and encoding
WebAssembly. It is not a runtime.

## Public API

The public entry points are in [watgo.go](../watgo.go):

- `ParseWAT`: WAT -> `wasmir.Module`
- `DecodeWASM`: binary wasm -> `wasmir.Module`
- `ValidateModule`: semantic validation over `wasmir.Module`
- `EncodeWASM`: `wasmir.Module` -> binary wasm
- `PrintWAT`: `wasmir.Module` -> WAT
- `CompileWATToWASM`: parse + lower + validate + encode

## Internal Structure

- `wasmir`: semantic IR and public IR types
- `internal/textformat`: WAT parsing and lowering
- `internal/binaryformat`: wasm binary decoding/encoding
- `internal/printer`: WAT printing from `wasmir`
- `internal/validate`: semantic validation
- `internal/instrdef`: shared instruction catalog used by text, binary, and
  validation code

The main pipeline is:

1. WAT -> `textformat` -> `wasmir`
2. wasm binary -> `binaryformat` -> `wasmir`
3. `wasmir` -> `validate`
4. `wasmir` -> `binaryformat` encoder
5. `wasmir` -> `printer` -> WAT

`wasmir` is the canonical semantic representation. Text-specific source details
such as folded syntax and literal spelling are intentionally not preserved
there. Binary name-section metadata is preserved where `wasmir` has explicit
name fields.

## Testing

- Unit tests cover parser, encoder/decoder, validator, and CLI layers.
- `tests/wasmspec` runs `.wast` scripts against `watgo`.
- The wasmspec harness uses Node as the execution engine, because `watgo` does
  not execute wasm modules itself.
- Detailed wasmspec tracing is off by default and can be enabled with
  `WATGO_WASMSPEC_DEBUG=1`.
