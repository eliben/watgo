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

(module (func (f32.const 0123456789) drop))
(module (func (f32.const 0123456789e019) drop))
(module (func (f32.const 0123456789e+019) drop))
(module (func (f32.const 0123456789e-019) drop))
(module (func (f32.const 0123456789.) drop))
(module (func (f32.const 0123456789.e019) drop))
(module (func (f32.const 0123456789.e+019) drop))
(module (func (f32.const 0123456789.e-019) drop))
(module (func (f32.const 0123456789.0123456789) drop))
(module (func (f32.const 0123456789.0123456789e019) drop))
(module (func (f32.const 0123456789.0123456789e+019) drop))
(module (func (f32.const 0123456789.0123456789e-019) drop))
(module (func (f32.const 0x0123456789ABCDEF) drop))
(module (func (f32.const 0x0123456789ABCDEFp019) drop))
(module (func (f32.const 0x0123456789ABCDEFp+019) drop))
(module (func (f32.const 0x0123456789ABCDEFp-019) drop))
(module (func (f32.const 0x0123456789ABCDEF.) drop))
(module (func (f32.const 0x0123456789ABCDEF.p019) drop))
(module (func (f32.const 0x0123456789ABCDEF.p+019) drop))
(module (func (f32.const 0x0123456789ABCDEF.p-019) drop))
(module (func (f32.const 0x0123456789ABCDEF.019aF) drop))
(module (func (f32.const 0x0123456789ABCDEF.019aFp019) drop))
(module (func (f32.const 0x0123456789ABCDEF.019aFp+019) drop))
(module (func (f32.const 0x0123456789ABCDEF.019aFp-019) drop))
(assert_malformed
  (module quote "(func (f32.const) drop)")
  "unexpected token"
)
(assert_malformed
  (module quote "(func (f32.const .0) drop)")
  "unknown operator"
)
(assert_malformed
  (module quote "(func (f32.const .0e0) drop)")
  "unknown operator"
)
(assert_malformed
  (module quote "(func (f32.const 0e) drop)")
  "unknown operator"
)
(assert_malformed
  (module quote "(func (f32.const 0e+) drop)")
  "unknown operator"
)
(assert_malformed
  (module quote "(func (f32.const 0.0e) drop)")
  "unknown operator"
)
(assert_malformed
  (module quote "(func (f32.const 0.0e-) drop)")
  "unknown operator"
)
(assert_malformed
  (module quote "(func (f32.const 0x) drop)")
  "unknown operator"
)
(assert_malformed
  (module quote "(func (f32.const 1x) drop)")
  "unknown operator"
)
(assert_malformed
  (module quote "(func (f32.const 0xg) drop)")
  "unknown operator"
)
(assert_malformed
  (module quote "(func (f32.const 0x.) drop)")
  "unknown operator"
)
(assert_malformed
  (module quote "(func (f32.const 0x0.g) drop)")
  "unknown operator"
)
(assert_malformed
  (module quote "(func (f32.const 0x0p) drop)")
  "unknown operator"
)
(assert_malformed
  (module quote "(func (f32.const 0x0p+) drop)")
  "unknown operator"
)
(assert_malformed
  (module quote "(func (f32.const 0x0p-) drop)")
  "unknown operator"
)
(assert_malformed
  (module quote "(func (f32.const 0x0.0p) drop)")
  "unknown operator"
)
(assert_malformed
  (module quote "(func (f32.const 0x0.0p+) drop)")
  "unknown operator"
)
(assert_malformed
  (module quote "(func (f32.const 0x0.0p-) drop)")
  "unknown operator"
)
(assert_malformed
  (module quote "(func (f32.const 0x0pA) drop)")
  "unknown operator"
)

