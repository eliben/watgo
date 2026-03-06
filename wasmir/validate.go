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
	case InstrEnd:
		return "end"
	default:
		return "unknown"
	}
}
