;; i32 operations

(module
  (func (export "add") (param $x i32) (param $y i32) (result i32) (i32.add (local.get $x) (local.get $y)))
  (func (export "sub") (param $x i32) (param $y i32) (result i32) (i32.sub (local.get $x) (local.get $y)))
  (func (export "mul") (param $x i32) (param $y i32) (result i32) (i32.mul (local.get $x) (local.get $y)))
  (func (export "div_s") (param $x i32) (param $y i32) (result i32) (i32.div_s (local.get $x) (local.get $y)))
  (func (export "div_u") (param $x i32) (param $y i32) (result i32) (i32.div_u (local.get $x) (local.get $y)))
)

(assert_return (invoke "add" (i32.const 1) (i32.const 1)) (i32.const 2))
(assert_return (invoke "add" (i32.const 1) (i32.const 0)) (i32.const 1))
(assert_return (invoke "add" (i32.const -1) (i32.const -1)) (i32.const -2))
(assert_return (invoke "add" (i32.const -1) (i32.const 1)) (i32.const 0))
(assert_return (invoke "add" (i32.const 0x7fffffff) (i32.const 1)) (i32.const 0x80000000))

(assert_return (invoke "sub" (i32.const 1) (i32.const 1)) (i32.const 0))
(assert_return (invoke "sub" (i32.const 1) (i32.const 0)) (i32.const 1))
(assert_return (invoke "sub" (i32.const -1) (i32.const -1)) (i32.const 0))
(assert_return (invoke "sub" (i32.const 0x7fffffff) (i32.const -1)) (i32.const 0x80000000))

(assert_return (invoke "mul" (i32.const 1) (i32.const 1)) (i32.const 1))
(assert_return (invoke "mul" (i32.const 1) (i32.const 0)) (i32.const 0))
(assert_return (invoke "mul" (i32.const -1) (i32.const -1)) (i32.const 1))
(assert_return (invoke "mul" (i32.const 0x10000000) (i32.const 4096)) (i32.const 0))
(assert_return (invoke "mul" (i32.const 0x80000000) (i32.const 0)) (i32.const 0))

(assert_trap (invoke "div_s" (i32.const 1) (i32.const 0)) "integer divide by zero")
(assert_trap (invoke "div_s" (i32.const 0) (i32.const 0)) "integer divide by zero")
(assert_trap (invoke "div_s" (i32.const 0x80000000) (i32.const -1)) "integer overflow")

(assert_trap (invoke "div_u" (i32.const 1) (i32.const 0)) "integer divide by zero")
(assert_trap (invoke "div_u" (i32.const 0) (i32.const 0)) "integer divide by zero")
(assert_return (invoke "div_u" (i32.const 1) (i32.const 1)) (i32.const 1))
(assert_return (invoke "div_u" (i32.const 0) (i32.const 1)) (i32.const 0))
(assert_return (invoke "div_u" (i32.const -1) (i32.const -1)) (i32.const 1))
(assert_return (invoke "div_u" (i32.const 0x80000000) (i32.const -1)) (i32.const 0))

(assert_invalid
  (module
    (func $type-unary-operand-empty
      (i32.eqz) (drop)
    )
  )
  "type mismatch"
)
(assert_invalid
  (module
    (func $type-unary-operand-empty-in-block
      (i32.const 0)
      (block (i32.eqz) (drop))
    )
  )
  "type mismatch"
)

;; Type check

(assert_invalid (module (func (result i32) (i32.add (i64.const 0) (f32.const 0)))) "type mismatch")

(assert_malformed
  (module quote "(func (result i32) (i32.const nan:arithmetic))")
  "unexpected token"
)
(assert_malformed
  (module quote "(func (result i32) (i32.const nan:canonical))")
  "unexpected token"
)
