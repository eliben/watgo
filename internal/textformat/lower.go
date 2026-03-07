package textformat

import (
	"strconv"
	"strings"

	"github.com/eliben/watgo/diag"
	"github.com/eliben/watgo/wasmir"
)

// LowerModule lowers astm (a parsed text-format module) into a semantic
// wasmir.Module.
// It returns the lowered module (possibly partial) and nil on success.
// On any failure, it returns diag.ErrorList.
func LowerModule(astm *Module) (*wasmir.Module, error) {
	if astm == nil {
		return nil, diag.Fromf("module is nil")
	}

	var diags diag.ErrorList
	out := &wasmir.Module{}

	for i, f := range astm.Funcs {
		if f == nil {
			diags.Addf("func[%d]: nil function", i)
			continue
		}
		lowerFunction(f, i, out, &diags)
	}

	if diags.HasAny() {
		return out, diags
	}
	return out, nil
}

// lowerFunction lowers f into out as function number funcIdx, appending any
// diagnostics into diags.
func lowerFunction(f *Function, funcIdx int, out *wasmir.Module, diags *diag.ErrorList) {
	var params []wasmir.ValueType
	var results []wasmir.ValueType
	var locals []wasmir.ValueType

	localsByName := map[string]uint32{}
	nextLocalIndex := uint32(0)

	for _, pd := range f.TyUse.Params {
		if pd == nil {
			diags.Addf("func[%d]: nil param declaration", funcIdx)
			continue
		}
		vt, ok := lowerValueType(pd.Ty)
		if !ok {
			addLowerDiag(diags, funcIdx, pd.loc.String(), "unsupported param type %q", pd.Ty)
			continue
		}
		params = append(params, vt)

		if pd.Id != "" {
			if _, exists := localsByName[pd.Id]; exists {
				addLowerDiag(diags, funcIdx, pd.loc.String(), "duplicate param id %q", pd.Id)
			} else {
				localsByName[pd.Id] = nextLocalIndex
			}
		}
		nextLocalIndex++
	}

	for _, rd := range f.TyUse.Results {
		if rd == nil {
			diags.Addf("func[%d]: nil result declaration", funcIdx)
			continue
		}
		vt, ok := lowerValueType(rd.Ty)
		if !ok {
			addLowerDiag(diags, funcIdx, rd.loc.String(), "unsupported result type %q", rd.Ty)
			continue
		}
		results = append(results, vt)
	}

	for _, ld := range f.Locals {
		if ld == nil {
			diags.Addf("func[%d]: nil local declaration", funcIdx)
			continue
		}
		vt, ok := lowerValueType(ld.Ty)
		if !ok {
			addLowerDiag(diags, funcIdx, ld.loc.String(), "unsupported local type %q", ld.Ty)
			continue
		}
		locals = append(locals, vt)

		if ld.Id != "" {
			if _, exists := localsByName[ld.Id]; exists {
				addLowerDiag(diags, funcIdx, ld.loc.String(), "duplicate local id %q", ld.Id)
			} else {
				localsByName[ld.Id] = nextLocalIndex
			}
		}
		nextLocalIndex++
	}

	typeIdx := uint32(len(out.Types))
	out.Types = append(out.Types, wasmir.FuncType{Params: params, Results: results})

	body := lowerInstrs(f.Instrs, funcIdx, localsByName, diags)
	body = append(body, wasmir.Instruction{Kind: wasmir.InstrEnd, SourceLoc: f.loc.String()})

	out.Funcs = append(out.Funcs, wasmir.Function{
		TypeIdx:   typeIdx,
		Locals:    locals,
		Body:      body,
		SourceLoc: f.loc.String(),
	})

	if f.Export != "" {
		out.Exports = append(out.Exports, wasmir.Export{
			Name:  f.Export,
			Kind:  wasmir.ExternalKindFunction,
			Index: uint32(len(out.Funcs) - 1),
		})
	}
}

// lowerInstrs lowers instrs for function funcIdx.
// localsByName maps text local identifiers to semantic local indices.
// It returns lowered instructions (without the implicit final end) and appends
// diagnostics into diags.
func lowerInstrs(instrs []Instruction, funcIdx int, localsByName map[string]uint32, diags *diag.ErrorList) []wasmir.Instruction {
	out := make([]wasmir.Instruction, 0, len(instrs)+1)

	for _, instr := range instrs {
		pi, ok := instr.(*PlainInstr)
		if !ok {
			diags.Addf("func[%d]: unsupported instruction type %T", funcIdx, instr)
			continue
		}
		instrLoc := pi.Loc()

		switch pi.Name {
		case "local.get":
			if len(pi.Operands) != 1 {
				addLowerDiag(diags, funcIdx, instrLoc, "local.get expects 1 operand")
				continue
			}

			localIndex, ok := lowerLocalIndexOperand(pi.Operands[0], localsByName)
			if !ok {
				addLowerDiag(diags, funcIdx, pi.Operands[0].Loc(), "invalid local.get operand")
				continue
			}
			out = append(out, wasmir.Instruction{Kind: wasmir.InstrLocalGet, LocalIndex: localIndex, SourceLoc: instrLoc})

		case "i32.const":
			if len(pi.Operands) != 1 {
				addLowerDiag(diags, funcIdx, instrLoc, "i32.const expects 1 operand")
				continue
			}
			imm, ok := lowerI32ConstOperand(pi.Operands[0])
			if !ok {
				addLowerDiag(diags, funcIdx, pi.Operands[0].Loc(), "invalid i32.const operand")
				continue
			}
			out = append(out, wasmir.Instruction{Kind: wasmir.InstrI32Const, I32Const: imm, SourceLoc: instrLoc})

		case "i64.const":
			if len(pi.Operands) != 1 {
				addLowerDiag(diags, funcIdx, instrLoc, "i64.const expects 1 operand")
				continue
			}
			imm, ok := lowerI64ConstOperand(pi.Operands[0])
			if !ok {
				addLowerDiag(diags, funcIdx, pi.Operands[0].Loc(), "invalid i64.const operand")
				continue
			}
			out = append(out, wasmir.Instruction{Kind: wasmir.InstrI64Const, I64Const: imm, SourceLoc: instrLoc})

		case "drop":
			if len(pi.Operands) != 0 {
				addLowerDiag(diags, funcIdx, instrLoc, "drop expects no operands")
				continue
			}
			out = append(out, wasmir.Instruction{Kind: wasmir.InstrDrop, SourceLoc: instrLoc})

		case "i32.add":
			if len(pi.Operands) != 0 {
				addLowerDiag(diags, funcIdx, instrLoc, "i32.add expects no operands")
				continue
			}
			out = append(out, wasmir.Instruction{Kind: wasmir.InstrI32Add, SourceLoc: instrLoc})

		case "i32.sub":
			if len(pi.Operands) != 0 {
				addLowerDiag(diags, funcIdx, instrLoc, "i32.sub expects no operands")
				continue
			}
			out = append(out, wasmir.Instruction{Kind: wasmir.InstrI32Sub, SourceLoc: instrLoc})

		case "i32.mul":
			if len(pi.Operands) != 0 {
				addLowerDiag(diags, funcIdx, instrLoc, "i32.mul expects no operands")
				continue
			}
			out = append(out, wasmir.Instruction{Kind: wasmir.InstrI32Mul, SourceLoc: instrLoc})

		case "i32.div_s":
			if len(pi.Operands) != 0 {
				addLowerDiag(diags, funcIdx, instrLoc, "i32.div_s expects no operands")
				continue
			}
			out = append(out, wasmir.Instruction{Kind: wasmir.InstrI32DivS, SourceLoc: instrLoc})

		case "i32.div_u":
			if len(pi.Operands) != 0 {
				addLowerDiag(diags, funcIdx, instrLoc, "i32.div_u expects no operands")
				continue
			}
			out = append(out, wasmir.Instruction{Kind: wasmir.InstrI32DivU, SourceLoc: instrLoc})

		case "i64.add":
			if len(pi.Operands) != 0 {
				addLowerDiag(diags, funcIdx, instrLoc, "i64.add expects no operands")
				continue
			}
			out = append(out, wasmir.Instruction{Kind: wasmir.InstrI64Add, SourceLoc: instrLoc})

		case "i64.sub":
			if len(pi.Operands) != 0 {
				addLowerDiag(diags, funcIdx, instrLoc, "i64.sub expects no operands")
				continue
			}
			out = append(out, wasmir.Instruction{Kind: wasmir.InstrI64Sub, SourceLoc: instrLoc})

		case "i64.mul":
			if len(pi.Operands) != 0 {
				addLowerDiag(diags, funcIdx, instrLoc, "i64.mul expects no operands")
				continue
			}
			out = append(out, wasmir.Instruction{Kind: wasmir.InstrI64Mul, SourceLoc: instrLoc})

		case "i64.div_s":
			if len(pi.Operands) != 0 {
				addLowerDiag(diags, funcIdx, instrLoc, "i64.div_s expects no operands")
				continue
			}
			out = append(out, wasmir.Instruction{Kind: wasmir.InstrI64DivS, SourceLoc: instrLoc})

		case "i64.div_u":
			if len(pi.Operands) != 0 {
				addLowerDiag(diags, funcIdx, instrLoc, "i64.div_u expects no operands")
				continue
			}
			out = append(out, wasmir.Instruction{Kind: wasmir.InstrI64DivU, SourceLoc: instrLoc})

		case "f32.add":
			if len(pi.Operands) != 0 {
				addLowerDiag(diags, funcIdx, instrLoc, "f32.add expects no operands")
				continue
			}
			out = append(out, wasmir.Instruction{Kind: wasmir.InstrF32Add, SourceLoc: instrLoc})

		case "f32.sub":
			if len(pi.Operands) != 0 {
				addLowerDiag(diags, funcIdx, instrLoc, "f32.sub expects no operands")
				continue
			}
			out = append(out, wasmir.Instruction{Kind: wasmir.InstrF32Sub, SourceLoc: instrLoc})

		case "f32.mul":
			if len(pi.Operands) != 0 {
				addLowerDiag(diags, funcIdx, instrLoc, "f32.mul expects no operands")
				continue
			}
			out = append(out, wasmir.Instruction{Kind: wasmir.InstrF32Mul, SourceLoc: instrLoc})

		case "f32.div":
			if len(pi.Operands) != 0 {
				addLowerDiag(diags, funcIdx, instrLoc, "f32.div expects no operands")
				continue
			}
			out = append(out, wasmir.Instruction{Kind: wasmir.InstrF32Div, SourceLoc: instrLoc})

		case "f32.sqrt":
			if len(pi.Operands) != 0 {
				addLowerDiag(diags, funcIdx, instrLoc, "f32.sqrt expects no operands")
				continue
			}
			out = append(out, wasmir.Instruction{Kind: wasmir.InstrF32Sqrt, SourceLoc: instrLoc})

		case "f32.min":
			if len(pi.Operands) != 0 {
				addLowerDiag(diags, funcIdx, instrLoc, "f32.min expects no operands")
				continue
			}
			out = append(out, wasmir.Instruction{Kind: wasmir.InstrF32Min, SourceLoc: instrLoc})

		case "f32.max":
			if len(pi.Operands) != 0 {
				addLowerDiag(diags, funcIdx, instrLoc, "f32.max expects no operands")
				continue
			}
			out = append(out, wasmir.Instruction{Kind: wasmir.InstrF32Max, SourceLoc: instrLoc})

		case "f32.ceil":
			if len(pi.Operands) != 0 {
				addLowerDiag(diags, funcIdx, instrLoc, "f32.ceil expects no operands")
				continue
			}
			out = append(out, wasmir.Instruction{Kind: wasmir.InstrF32Ceil, SourceLoc: instrLoc})

		case "f32.floor":
			if len(pi.Operands) != 0 {
				addLowerDiag(diags, funcIdx, instrLoc, "f32.floor expects no operands")
				continue
			}
			out = append(out, wasmir.Instruction{Kind: wasmir.InstrF32Floor, SourceLoc: instrLoc})

		case "f32.trunc":
			if len(pi.Operands) != 0 {
				addLowerDiag(diags, funcIdx, instrLoc, "f32.trunc expects no operands")
				continue
			}
			out = append(out, wasmir.Instruction{Kind: wasmir.InstrF32Trunc, SourceLoc: instrLoc})

		case "f32.nearest":
			if len(pi.Operands) != 0 {
				addLowerDiag(diags, funcIdx, instrLoc, "f32.nearest expects no operands")
				continue
			}
			out = append(out, wasmir.Instruction{Kind: wasmir.InstrF32Nearest, SourceLoc: instrLoc})

		default:
			addLowerDiag(diags, funcIdx, instrLoc, "unsupported instruction %q", pi.Name)
		}
	}

	return out
}

// lowerI32ConstOperand resolves op as an i32.const immediate.
// It returns the immediate value and true on success.
func lowerI32ConstOperand(op Operand) (int32, bool) {
	o, ok := op.(*IntOperand)
	if !ok {
		return 0, false
	}

	bits, ok := parseIntLiteralBits(o.Value, 32)
	if !ok {
		return 0, false
	}
	return int32(bits), true
}

// lowerI64ConstOperand resolves op as an i64.const immediate.
// It returns the immediate value and true on success.
func lowerI64ConstOperand(op Operand) (int64, bool) {
	o, ok := op.(*IntOperand)
	if !ok {
		return 0, false
	}

	bits, ok := parseIntLiteralBits(o.Value, 64)
	if !ok {
		return 0, false
	}
	return int64(bits), true
}

// lowerLocalIndexOperand resolves op as a local index using localsByName.
// It returns the resolved index and true on success, or 0/false otherwise.
func lowerLocalIndexOperand(op Operand, localsByName map[string]uint32) (uint32, bool) {
	switch o := op.(type) {
	case *IdOperand:
		idx, ok := localsByName[o.Value]
		return idx, ok
	case *IntOperand:
		return parseU32Literal(o.Value)
	default:
		return 0, false
	}
}

// parseIntLiteralBits parses s as an integer literal and returns its
// two's-complement bits in the requested width (32 or 64).
func parseIntLiteralBits(s string, bits int) (uint64, bool) {
	if bits != 32 && bits != 64 {
		return 0, false
	}

	clean := strings.ReplaceAll(s, "_", "")
	neg := false
	if len(clean) > 0 {
		switch clean[0] {
		case '+':
			clean = clean[1:]
		case '-':
			neg = true
			clean = clean[1:]
		}
	}
	if clean == "" {
		return 0, false
	}

	base := 10
	if strings.HasPrefix(clean, "0x") || strings.HasPrefix(clean, "0X") {
		base = 16
		clean = clean[2:]
		if clean == "" {
			return 0, false
		}
	}

	u, err := strconv.ParseUint(clean, base, bits)
	if err != nil {
		return 0, false
	}
	if neg {
		u = ^u + 1
	}
	if bits == 32 {
		u &= (1 << 32) - 1
	}
	return u, true
}

// parseU32Literal parses s as an unsigned 32-bit integer literal.
// It returns the parsed value and true on success, or 0/false on failure.
func parseU32Literal(s string) (uint32, bool) {
	clean := strings.ReplaceAll(s, "_", "")
	value, err := strconv.ParseInt(clean, 0, 64)
	if err != nil || value < 0 || value > (1<<32-1) {
		return 0, false
	}
	return uint32(value), true
}

// lowerValueType lowers ty from textformat type syntax into semantic wasmir
// type representation.
// It returns the lowered type and true on success, or 0/false if ty is
// unsupported.
func lowerValueType(ty Type) (wasmir.ValueType, bool) {
	bt, ok := ty.(*BasicType)
	if !ok {
		return 0, false
	}

	switch bt.Name {
	case "i32":
		return wasmir.ValueTypeI32, true
	case "i64":
		return wasmir.ValueTypeI64, true
	case "f32":
		return wasmir.ValueTypeF32, true
	default:
		return 0, false
	}
}

// addLowerDiag appends one lowering diagnostic prefixed with function context
// and optional source location.
// If loc is non-empty, the message format is:
//
//	"func[%d] at <loc>: <message>"
//
// Otherwise:
//
//	"func[%d]: <message>"
func addLowerDiag(diags *diag.ErrorList, funcIdx int, loc string, format string, args ...any) {
	allArgs := append([]any{funcIdx}, args...)
	if loc != "" {
		diags.Addf("func[%d] at %s: "+format, append([]any{funcIdx, loc}, args...)...)
		return
	}
	diags.Addf("func[%d]: "+format, allArgs...)
}
