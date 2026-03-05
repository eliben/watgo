# watgo Design

## Goals

`watgo` is a pure-Go toolkit for WebAssembly that can:

- Parse WAT text into an internal representation.
- Parse WASM binary into an internal representation.
- Emit WAT text and WASM binary from that representation.
- Support transforms, analysis, debugging, and inspection.

This design aims for correctness first, then ergonomics and performance.

## Non-Goals (initially)

- Full engine/runtime execution of modules.
- JIT/AOT compilation.
- Support for all proposals from day one.

## Architectural Principles

- Single canonical semantic IR to represent WASM modules, no matter which format
  they came from (text, binary).
- Lossless-ish text AST for WAT source fidelity (names, folded forms,
  formatting-sensitive details where needed).
- Strict separation between parsing/validation/encoding.
- Explicit feature gating (MVP = core spec; proposals opt-in).
- Deterministic outputs for stable tests and debugging.

## High-Level Pipeline

1. WAT input -> text lexer/parser -> `textformat` (text-oriented AST).
2. `textformat` -> lower -> `wasmir` (canonical semantic IR).
3. `wasmir` -> validator -> validated IR (or diagnostic set).
4. `wasmir` -> binary encoder -> WASM bytes.
5. WASM bytes -> binary decoder -> `wasmir`.
6. `wasmir` -> text printer -> WAT (canonical pretty-printed form).

## Suggested Package Layout

Keep packages small and composable:

- `internal/wasmir`
  - Canonical semantic module IR.
  - Index-based references (typeidx/funcidx/localidx/etc).
  - No text-only constructs.
  - Validation
- `internal/textformat`
  - Text-format AST preserving names, folded instructions, syntactic sugar.
  - Lexer + parser
- `internal/binaryformat`
  - Decoder: WASM bytes -> `wasmir`.
  - Encoder: `wasmir` -> WASM bytes.
  - Expands folded syntax into linear instruction sequences.
- `internal/features`
  - Feature flags (MVP + proposals).
- `cmd/watgo` (later)
  - CLI (`parse`, `validate`, `wat2wasm`, `wasm2wat`, `dump`, etc).

## IR Design

### `textformat` (text AST)

Use this for faithful text parsing:

- Module/function declarations with identifiers (`$name`) preserved.
- Folded instructions preserved structurally.
- Type-use syntax preserved where useful for diagnostics.
- Source spans on all nodes for precise errors.

### `wasmir` (semantic IR)

Use this as the canonical transformation/emission target:

- Fully resolved indices (no unresolved `$name`).
- Explicit sections/components:
  - Types, imports, functions, tables, memories, globals, exports, start,
    elements, data, customs.
- Function bodies in normalized instruction form (unfolded).
- Constants/immediates represented in typed form.
- Optional metadata map for debug/provenance.

This split avoids polluting the semantic IR with text-only syntax concerns.

## Parsing and Lowering Strategy

### WAT Parser

- Build robust tokenizer first (comments, strings, numbers, keywords, IDs).
- Parse S-expression structure.
- Build ASTs for module items and instructions.
- Keep parser permissive enough to collect multiple errors in one pass.

### Lowerer

- Build symbol tables per index space:
  - types, funcs, tables, memories, globals, locals, labels.
- Resolve names to indices with clear diagnostics.
- Convert folded instructions to linear form.
- Normalize shorthand where possible.

### Binary Decoder/Encoder

- Decoder should preserve semantics; ignore/retain custom sections based on option.
- Encoder should produce canonical section ordering and deterministic output.
- For unsupported/unknown features, produce typed errors tied to feature flags.

## Validation Strategy

Validation should be explicit and reusable (not hidden in parser/decoder):

- Type validation of instructions and blocks.
- Index bounds checks.
- Import/export consistency.
- Start function constraints.
- Memory/table/global constraints.

## Testing Strategy

### 1. Unit Tests

- Lexer/token tests (valid + malformed).
- Parser node-shape tests for WAT constructs.
- Resolver tests for name/index resolution edge cases.
- Binary codec tests per section and instruction immediate.
- Validator rule tests by category.

Use table-driven tests heavily.

### 2. End-to-end testing with wasm spec

Parse scripts from WebAssembly/spec to extract expected semantics, run them
and compare. Can use a command-line runtime like 'node' or something similar
to execute.

WASM spec tests live in https://github.com/WebAssembly/spec/tree/main/test/core
Their own runner uses the wasm interpreter in that repo (written in OCaml):
https://github.com/WebAssembly/spec/tree/main/interpreter

This is the syntax for `*.wast` scripts:
https://github.com/WebAssembly/spec/tree/main/interpreter#scripts

## Notes

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
