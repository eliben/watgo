;; Test `call` operator

(module
  ;; Auxiliary definitions
  (func $const-i32 (result i32) (i32.const 0x132))
  (func $const-i64 (result i64) (i64.const 0x164))
  (func $const-f32 (result f32) (f32.const 0xf32))
  (func $const-f64 (result f64) (f64.const 0xf64))
  (func $const-i32-i64 (result i32 i64) (i32.const 0x132) (i64.const 0x164))

  (func $id-i32 (param i32) (result i32) (local.get 0))
  (func $id-i64 (param i64) (result i64) (local.get 0))
  (func $id-f32 (param f32) (result f32) (local.get 0))
  (func $id-f64 (param f64) (result f64) (local.get 0))
  (func $id-i32-f64 (param i32 f64) (result i32 f64)
    (local.get 0) (local.get 1)
  )

  (func $swap-i32-i32 (param i32 i32) (result i32 i32)
    (local.get 1) (local.get 0)
  )
  (func $swap-f32-f64 (param f32 f64) (result f64 f32)
    (local.get 1) (local.get 0)
  )
  (func $swap-f64-i32 (param f64 i32) (result i32 f64)
    (local.get 1) (local.get 0)
  )

  (func $f32-i32 (param f32 i32) (result i32) (local.get 1))
  (func $i32-i64 (param i32 i64) (result i64) (local.get 1))
  (func $f64-f32 (param f64 f32) (result f32) (local.get 1))
  (func $i64-f64 (param i64 f64) (result f64) (local.get 1))

  ;; Typing

  (func (export "type-i32") (result i32) (call $const-i32))
  (func (export "type-i64") (result i64) (call $const-i64))
  (func (export "type-f32") (result f32) (call $const-f32))
  (func (export "type-f64") (result f64) (call $const-f64))
  (func (export "type-i32-i64") (result i32 i64) (call $const-i32-i64))

  (func (export "type-first-i32") (result i32) (call $id-i32 (i32.const 32)))
  (func (export "type-first-i64") (result i64) (call $id-i64 (i64.const 64)))
  (func (export "type-first-f32") (result f32) (call $id-f32 (f32.const 1.32)))
  (func (export "type-first-f64") (result f64) (call $id-f64 (f64.const 1.64)))

  (func (export "type-second-i32") (result i32)
    (call $f32-i32 (f32.const 32.1) (i32.const 32))
  )
  (func (export "type-second-i64") (result i64)
    (call $i32-i64 (i32.const 32) (i64.const 64))
  )
  (func (export "type-second-f32") (result f32)
    (call $f64-f32 (f64.const 64) (f32.const 32))
  )
  (func (export "type-second-f64") (result f64)
    (call $i64-f64 (i64.const 64) (f64.const 64.1))
  )

  (func (export "type-all-i32-f64") (result i32 f64)
    (call $id-i32-f64 (i32.const 32) (f64.const 1.64))
  )
  (func (export "type-all-i32-i32") (result i32 i32)
    (call $swap-i32-i32 (i32.const 1) (i32.const 2))
  )
  (func (export "type-all-f32-f64") (result f64 f32)
    (call $swap-f32-f64 (f32.const 1) (f64.const 2))
  )
  (func (export "type-all-f64-i32") (result i32 f64)
    (call $swap-f64-i32 (f64.const 1) (i32.const 2))
  )

  ;; Composition

  (func (export "as-binary-all-operands") (result i32)
    (i32.add (call $swap-i32-i32 (i32.const 3) (i32.const 4)))
  )

  (func (export "as-mixed-operands") (result i32)
    (call $swap-i32-i32 (i32.const 3) (i32.const 4))
    (i32.const 5)
    (i32.add)
    (i32.mul)
  )

  (func (export "as-call-all-operands") (result i32 i32)
    (call $swap-i32-i32 (call $swap-i32-i32 (i32.const 3) (i32.const 4)))
  )

  ;; Recursion

  (func $fac (export "fac") (param i64) (result i64)
    (if (result i64) (i64.eqz (local.get 0))
      (then (i64.const 1))
      (else
        (i64.mul
          (local.get 0)
          (call $fac (i64.sub (local.get 0) (i64.const 1)))
        )
      )
    )
  )
  (func $fac-acc (export "fac-acc") (param i64 i64) (result i64)
    (if (result i64) (i64.eqz (local.get 0))
      (then (local.get 1))
      (else
        (call $fac-acc
          (i64.sub (local.get 0) (i64.const 1))
          (i64.mul (local.get 0) (local.get 1))
        )
      )
    )
  )

  (func $fib (export "fib") (param i64) (result i64)
    (if (result i64) (i64.le_u (local.get 0) (i64.const 1))
      (then (i64.const 1))
      (else
        (i64.add
          (call $fib (i64.sub (local.get 0) (i64.const 2)))
          (call $fib (i64.sub (local.get 0) (i64.const 1)))
        )
      )
    )
  )

  (func $even (export "even") (param i64) (result i32)
    (if (result i32) (i64.eqz (local.get 0))
      (then (i32.const 44))
      (else (call $odd (i64.sub (local.get 0) (i64.const 1))))
    )
  )
  (func $odd (export "odd") (param i64) (result i32)
    (if (result i32) (i64.eqz (local.get 0))
      (then (i32.const 99))
      (else (call $even (i64.sub (local.get 0) (i64.const 1))))
    )
  )

)

(assert_return (invoke "type-i32") (i32.const 0x132))
(assert_return (invoke "type-i64") (i64.const 0x164))
(assert_return (invoke "type-f32") (f32.const 0xf32))
(assert_return (invoke "type-f64") (f64.const 0xf64))
(assert_return (invoke "type-i32-i64") (i32.const 0x132) (i64.const 0x164))

(assert_return (invoke "type-first-i32") (i32.const 32))
(assert_return (invoke "type-first-i64") (i64.const 64))
(assert_return (invoke "type-first-f32") (f32.const 1.32))
(assert_return (invoke "type-first-f64") (f64.const 1.64))

(assert_return (invoke "type-second-i32") (i32.const 32))
(assert_return (invoke "type-second-i64") (i64.const 64))
(assert_return (invoke "type-second-f32") (f32.const 32))
(assert_return (invoke "type-second-f64") (f64.const 64.1))

(assert_return (invoke "type-all-i32-f64") (i32.const 32) (f64.const 1.64))
(assert_return (invoke "type-all-i32-i32") (i32.const 2) (i32.const 1))
(assert_return (invoke "type-all-f32-f64") (f64.const 2) (f32.const 1))
(assert_return (invoke "type-all-f64-i32") (i32.const 2) (f64.const 1))

(assert_return (invoke "as-binary-all-operands") (i32.const 7))
(assert_return (invoke "as-mixed-operands") (i32.const 32))
(assert_return (invoke "as-call-all-operands") (i32.const 3) (i32.const 4))

(assert_return (invoke "fac" (i64.const 0)) (i64.const 1))
(assert_return (invoke "fac" (i64.const 1)) (i64.const 1))
(assert_return (invoke "fac" (i64.const 5)) (i64.const 120))
(assert_return (invoke "fac" (i64.const 25)) (i64.const 7034535277573963776))

(assert_return (invoke "fac-acc" (i64.const 0) (i64.const 1)) (i64.const 1))
(assert_return (invoke "fac-acc" (i64.const 1) (i64.const 1)) (i64.const 1))
(assert_return (invoke "fac-acc" (i64.const 5) (i64.const 1)) (i64.const 120))
(assert_return
  (invoke "fac-acc" (i64.const 25) (i64.const 1))
  (i64.const 7034535277573963776)
)

(assert_return (invoke "fib" (i64.const 0)) (i64.const 1))
(assert_return (invoke "fib" (i64.const 1)) (i64.const 1))
(assert_return (invoke "fib" (i64.const 2)) (i64.const 2))
(assert_return (invoke "fib" (i64.const 5)) (i64.const 8))
(assert_return (invoke "fib" (i64.const 20)) (i64.const 10946))

(assert_return (invoke "even" (i64.const 0)) (i32.const 44))
(assert_return (invoke "even" (i64.const 1)) (i32.const 99))
(assert_return (invoke "even" (i64.const 100)) (i32.const 44))
(assert_return (invoke "even" (i64.const 77)) (i32.const 99))
(assert_return (invoke "odd" (i64.const 0)) (i32.const 99))
(assert_return (invoke "odd" (i64.const 1)) (i32.const 44))
(assert_return (invoke "odd" (i64.const 200)) (i32.const 99))
(assert_return (invoke "odd" (i64.const 77)) (i32.const 44))

