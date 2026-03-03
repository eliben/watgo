# Dev notes

Identifiers that stand in for indices (e.g. `local.get $lhs`) aren't reflected
in the binary format at all. This leads me to think we'll need another
representation to parse the text format to; some sort of AST. Nested/folded
instructions (https://www.w3.org/TR/wasm-core-1/#folded-instructions%E2%91%A0)
is another such scenario. Something like an AST will be required to represent
the text format with high fidelity.

More details that are lost when lowering from text format:

* nested/folded instructions, and conditions like (if ... (then ...)) are also
  lowered to flat forms
* spelling of floating point numbers like 0.125 and other notations (in binary
  all floats are IEEE-754)
* names of vars / types
* inline types (instead of indices)

Realistic WAT code for testing:

* https://github.com/eliben/wasm-wat-samples/
* Game of life: https://github.com/ColinEberhardt/wasm-game-of-life/blob/master/main.wat
* Book samples:
  https://github.com/battlelinegames/ArtOfWasm
  https://github.com/bsletten/wasm_tdg
