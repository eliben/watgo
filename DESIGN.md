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

- Single canonical semantic IR for module-level meaning.
- Lossless-ish text AST for WAT source fidelity (names, folded forms, formatting-sensitive details where needed).
- Strict separation between parsing/validation/encoding.
- Explicit feature gating (MVP = core spec; proposals opt-in).
- Deterministic outputs for stable tests and debugging.

## High-Level Pipeline

1. WAT input -> text lexer/parser -> `watast` (text-oriented AST).
2. `watast` -> resolver/lowerer -> `wasmir` (canonical semantic IR).
3. `wasmir` -> validator -> validated IR (or diagnostic set).
4. `wasmir` -> binary encoder -> WASM bytes.
5. WASM bytes -> binary decoder -> `wasmir`.
6. `wasmir` -> text printer -> WAT (canonical pretty-printed form).

This allows:

- Text fidelity workflows: WAT -> `watast` -> pretty/debug transforms.
- Semantic workflows: WAT/Binary -> `wasmir` -> transforms -> emit text/binary.

## Suggested Package Layout

Keep packages small and composable:

- `internal/wasmir`
  - Canonical semantic module IR.
  - Index-based references (typeidx/funcidx/localidx/etc).
  - No text-only constructs.
- `internal/watast`
  - Text-format AST preserving names, folded instructions, syntactic sugar.
- `internal/textformat`
  - Lexer + parser to `watast`.
  - Printer from `watast` and/or from `wasmir` (canonical mode).
- `internal/binaryformat`
  - Decoder: WASM bytes -> `wasmir`.
  - Encoder: `wasmir` -> WASM bytes.
- `internal/resolve`
  - Name resolution and lowering: `watast` -> `wasmir`.
  - Expands folded syntax into linear instruction sequences.
- `internal/validate`
  - WebAssembly validation rules over `wasmir`.
- `internal/features`
  - Feature flags (MVP + proposals).
- `cmd/watgo` (later)
  - CLI (`parse`, `validate`, `wat2wasm`, `wasm2wat`, `dump`, etc).

## IR Design

### `watast` (text AST)

Use this for faithful text parsing:

- Module/function declarations with identifiers (`$name`) preserved.
- Folded instructions preserved structurally.
- Type-use syntax preserved where useful for diagnostics.
- Source spans on all nodes for precise errors.

### `wasmir` (semantic IR)

Use this as the canonical transformation/emission target:

- Fully resolved indices (no unresolved `$name`).
- Explicit sections/components:
  - Types, imports, functions, tables, memories, globals, exports, start, elements, data, customs.
- Function bodies in normalized instruction form (unfolded).
- Constants/immediates represented in typed form.
- Optional metadata map for debug/provenance.

This split avoids polluting the semantic IR with text-only syntax concerns.

## Parsing and Lowering Strategy

### WAT Parser

- Build robust tokenizer first (comments, strings, numbers, keywords, IDs).
- Parse S-expression structure.
- Build `watast` for module items and instructions.
- Keep parser permissive enough to collect multiple errors in one pass.

### Resolver/Lowerer

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

Expose both:

- `Validate(module, features) error`
- `ValidateAll(module, features) []Diagnostic`

## API Sketch (library-facing)

- `ParseWAT(src []byte, opts ParseOptions) (*watast.Module, []Diagnostic)`
- `LowerWAT(ast *watast.Module, opts LowerOptions) (*wasmir.Module, []Diagnostic)`
- `DecodeWASM(bin []byte, opts DecodeOptions) (*wasmir.Module, []Diagnostic)`
- `EncodeWASM(mod *wasmir.Module, opts EncodeOptions) ([]byte, []Diagnostic)`
- `PrintWAT(mod *wasmir.Module, opts PrintOptions) ([]byte, []Diagnostic)`
- `Validate(mod *wasmir.Module, opts ValidateOptions) []Diagnostic`

Consider convenience wrappers:

- `Wat2Wasm(src []byte, opts ToolOptions) ([]byte, []Diagnostic)`
- `Wasm2Wat(bin []byte, opts ToolOptions) ([]byte, []Diagnostic)`

## Testing Strategy

Use a layered test plan so bugs are caught close to source and also end-to-end.

### 1. Unit Tests (fast, focused)

- Lexer/token tests (valid + malformed).
- Parser node-shape tests for WAT constructs.
- Resolver tests for name/index resolution edge cases.
- Binary codec tests per section and instruction immediate.
- Validator rule tests by category.

Use table-driven tests heavily.

### 2. End-to-end testing with wasm spec

Parse scripts from WebAssembly/spec to extract expected semantics, run them
and compare. Can use a command-line runtime like 'node' or something similar
toe execute.
