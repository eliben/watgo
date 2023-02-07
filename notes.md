# Dev notes

Identifiers that stand in for indices (e.g. `local.get $lhs`) aren't reflected
in the binary format at all. This leads me to think we'll need another
representation to parse the text format to; some sort of AST. Nested/folded
instructions (https://www.w3.org/TR/wasm-core-1/#folded-instructions%E2%91%A0)
is another such scenario. Something like an AST will be required to represent
the text format with high fidelity.

The `wasm` package is the base, an abstract representation of the WASM module
with all its parts. `textformat` should depend on `wasm` because it will parse
the text format and emit `wasm` modules.
