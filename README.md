# watgo

The idea of this project was a Wasm Toolkit for Go (watgo) to parse WASM (text
and binary) into internal data structures, allowing conversions, etc.

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


