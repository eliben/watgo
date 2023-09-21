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

Realistic WAT code for testing:

* Game of life: https://github.com/ColinEberhardt/wasm-game-of-life/blob/master/main.wat
* Book samples:
  https://github.com/battlelinegames/ArtOfWasm
  https://github.com/bsletten/wasm_tdg

----

== Parsing

When I started getting deep into the parser, I've discovered that WAT (the text
format) is very messy and not fun to parse at all. Instructions have multiple
equivalent forms that require different strategies to parse - this isn't just
s-exprs, since the normal form of instructions is sequential, e.g. this uses
the "folded instruction" form for `if`:

```
(func $abs 
  (param $value i32) 
  (result i32)
  (if     
    (i32.lt_s (local.get $value) (i32.const 0))
    (then 
      i32.const 0
      local.get $value
      i32.sub
      return))
  local.get $value
)
```

But this is equivalent, using the non-folded form:

```
(func $abs 
  (param $value i32) 
  (result i32)
  (i32.lt_s (local.get $value) (i32.const 0))
  if    
  i32.const 0
  local.get $value
  i32.sub
  return
  end
  local.get $value
)
```

In non-folded forms the instructions are delineated by tokens like `end`; this
is an ugly mix.

To tackle this, I need to maintain at least a 2-token lookahead that can detect
sexprs like '(' 'if' etc.

