# Dev notes

The `wasm` package is the base, an abstract representation of the WASM module
with all its parts. `textformat` should depend on `wasm` because it will parse
the text format and emit `wasm` modules.
