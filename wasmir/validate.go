package wasmir

import "github.com/eliben/watgo/diag"

// ValidateModule validates m.
// Validation includes module-level checks (type/export indices) and function
// body type checks for the currently supported instruction subset.
// It returns nil on success. On any failure, it returns diag.ErrorList.
func ValidateModule(m *Module) error {
	if m == nil {
		return diag.Fromf("module is nil")
	}

	var diags diag.ErrorList
	for i, f := range m.Funcs {
		if int(f.TypeIdx) >= len(m.Types) {
			diags.Addf("func[%d] has invalid type index %d", i, f.TypeIdx)
			continue
		}
		funcErrs := validateFunctionBody(m.Types[f.TypeIdx], f)
		for _, err := range funcErrs {
			diags.Addf("func[%d]: %v", i, err)
		}
	}

	for i, exp := range m.Exports {
		if exp.Kind != ExternalKindFunction {
			diags.Addf("export[%d] has unsupported kind %d", i, exp.Kind)
			continue
		}
		if int(exp.Index) >= len(m.Funcs) {
			diags.Addf("export[%d] index %d out of range", i, exp.Index)
		}
	}

	if diags.HasAny() {
		return diags
	}
	return nil
}

// validateFunctionBody validates f against function type ft.
// It returns all diagnostics found while checking instruction ordering,
// local-index bounds and stack/result typing.
func validateFunctionBody(ft FuncType, f Function) diag.ErrorList {
	var diags diag.ErrorList

	if len(f.Body) == 0 {
		diags.Addf("empty function body")
		return diags
	}
	if f.Body[len(f.Body)-1].Kind != InstrEnd {
		diags.Addf("function body must terminate with end")
		return diags
	}

	locals := make([]ValueType, 0, len(ft.Params)+len(f.Locals))
	locals = append(locals, ft.Params...)
	locals = append(locals, f.Locals...)

	stack := make([]ValueType, 0)

	for i, ins := range f.Body {
		switch ins.Kind {
		case InstrLocalGet:
			if int(ins.LocalIndex) >= len(locals) {
				diags.Addf("instruction %d: local index %d out of range", i, ins.LocalIndex)
				continue
			}
			stack = append(stack, locals[ins.LocalIndex])

		case InstrI32Const:
			stack = append(stack, ValueTypeI32)

		case InstrI64Const:
			stack = append(stack, ValueTypeI64)

		case InstrDrop:
			if len(stack) < 1 {
				diags.Addf("instruction %d: drop needs 1 operand", i)
				continue
			}
			stack = stack[:len(stack)-1]

		case InstrI32Add, InstrI32Sub, InstrI32Mul, InstrI32DivS, InstrI32DivU:
			name := instrName(ins.Kind)
			if len(stack) < 2 {
				diags.Addf("instruction %d: %s needs 2 operands", i, name)
				continue
			}
			if stack[len(stack)-1] != ValueTypeI32 || stack[len(stack)-2] != ValueTypeI32 {
				diags.Addf("instruction %d: %s expects i32 operands", i, name)
				continue
			}
			stack = stack[:len(stack)-2]
			stack = append(stack, ValueTypeI32)

		case InstrI64Add, InstrI64Sub, InstrI64Mul, InstrI64DivS, InstrI64DivU:
			name := instrName(ins.Kind)
			if len(stack) < 2 {
				diags.Addf("instruction %d: %s needs 2 operands", i, name)
				continue
			}
			if stack[len(stack)-1] != ValueTypeI64 || stack[len(stack)-2] != ValueTypeI64 {
				diags.Addf("instruction %d: %s expects i64 operands", i, name)
				continue
			}
			stack = stack[:len(stack)-2]
			stack = append(stack, ValueTypeI64)

		case InstrF32Add, InstrF32Sub, InstrF32Mul, InstrF32Div, InstrF32Min, InstrF32Max:
			name := instrName(ins.Kind)
			if len(stack) < 2 {
				diags.Addf("instruction %d: %s needs 2 operands", i, name)
				continue
			}
			if stack[len(stack)-1] != ValueTypeF32 || stack[len(stack)-2] != ValueTypeF32 {
				diags.Addf("instruction %d: %s expects f32 operands", i, name)
				continue
			}
			stack = stack[:len(stack)-2]
			stack = append(stack, ValueTypeF32)

		case InstrF32Sqrt, InstrF32Ceil, InstrF32Floor, InstrF32Trunc, InstrF32Nearest:
			name := instrName(ins.Kind)
			if len(stack) < 1 {
				diags.Addf("instruction %d: %s needs 1 operand", i, name)
				continue
			}
			if stack[len(stack)-1] != ValueTypeF32 {
				diags.Addf("instruction %d: %s expects f32 operand", i, name)
				continue
			}
			// Unary f32 operators preserve top-of-stack type.

		case InstrEnd:
			if i != len(f.Body)-1 {
				diags.Addf("instruction %d: end must be last", i)
			}

		default:
			diags.Addf("instruction %d: unsupported instruction kind %d", i, ins.Kind)
		}
	}

	if len(stack) != len(ft.Results) {
		diags.Addf("result arity mismatch: got %d stack values, want %d", len(stack), len(ft.Results))
		return diags
	}
	for i := range stack {
		if stack[i] != ft.Results[i] {
			diags.Addf("result type mismatch at %d: got %d want %d", i, stack[i], ft.Results[i])
		}
	}

	return diags
}

func instrName(kind InstrKind) string {
	switch kind {
	case InstrLocalGet:
		return "local.get"
	case InstrI32Const:
		return "i32.const"
	case InstrI64Const:
		return "i64.const"
	case InstrDrop:
		return "drop"
	case InstrI32Add:
		return "i32.add"
	case InstrI32Sub:
		return "i32.sub"
	case InstrI32Mul:
		return "i32.mul"
	case InstrI32DivS:
		return "i32.div_s"
	case InstrI32DivU:
		return "i32.div_u"
	case InstrI64Add:
		return "i64.add"
	case InstrI64Sub:
		return "i64.sub"
	case InstrI64Mul:
		return "i64.mul"
	case InstrI64DivS:
		return "i64.div_s"
	case InstrI64DivU:
		return "i64.div_u"
	case InstrF32Add:
		return "f32.add"
	case InstrF32Sub:
		return "f32.sub"
	case InstrF32Mul:
		return "f32.mul"
	case InstrF32Div:
		return "f32.div"
	case InstrF32Sqrt:
		return "f32.sqrt"
	case InstrF32Min:
		return "f32.min"
	case InstrF32Max:
		return "f32.max"
	case InstrF32Ceil:
		return "f32.ceil"
	case InstrF32Floor:
		return "f32.floor"
	case InstrF32Trunc:
		return "f32.trunc"
	case InstrF32Nearest:
		return "f32.nearest"
	case InstrEnd:
		return "end"
	default:
		return "unknown"
	}
}
