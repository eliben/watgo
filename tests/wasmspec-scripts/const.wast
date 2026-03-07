;; Test t.const instructions

;; Syntax error

(module (func (i32.const 0_123_456_789) drop))
(module (func (i32.const 0x0_9acf_fBDF) drop))
(assert_malformed
  (module quote "(func (i32.const) drop)")
  "unexpected token"
)
(assert_malformed
  (module quote "(func (i32.const 0x) drop)")
  "unknown operator"
)
(assert_malformed
  (module quote "(func (i32.const 1x) drop)")
  "unknown operator"
)
(assert_malformed
  (module quote "(func (i32.const 0xg) drop)")
  "unknown operator"
)

(module (func (i64.const 0_123_456_789) drop))
(module (func (i64.const 0x0125_6789_ADEF_bcef) drop))
(assert_malformed
  (module quote "(func (i64.const) drop)")
  "unexpected token"
)
(assert_malformed
  (module quote "(func (i64.const 0x) drop)")
  "unknown operator"
)
(assert_malformed
  (module quote "(func (i64.const 1x) drop)")
  "unknown operator"
)
(assert_malformed
  (module quote "(func (i64.const 0xg) drop)")
  "unknown operator"
)

