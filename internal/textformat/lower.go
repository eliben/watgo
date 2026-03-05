package textformat

import (
	"strconv"
	"strings"

	"github.com/eliben/watgo/internal/diag"
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
			diags.Addf("func[%d]: unsupported param type %q", funcIdx, pd.Ty)
			continue
		}
		params = append(params, vt)

		if pd.Id != "" {
			if _, exists := localsByName[pd.Id]; exists {
				diags.Addf("func[%d]: duplicate param id %q", funcIdx, pd.Id)
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
			diags.Addf("func[%d]: unsupported result type %q", funcIdx, rd.Ty)
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
			diags.Addf("func[%d]: unsupported local type %q", funcIdx, ld.Ty)
			continue
		}
		locals = append(locals, vt)

		if ld.Id != "" {
			if _, exists := localsByName[ld.Id]; exists {
				diags.Addf("func[%d]: duplicate local id %q", funcIdx, ld.Id)
			} else {
				localsByName[ld.Id] = nextLocalIndex
			}
		}
		nextLocalIndex++
	}

	typeIdx := uint32(len(out.Types))
	out.Types = append(out.Types, wasmir.FuncType{Params: params, Results: results})

	body := lowerInstrs(f.Instrs, funcIdx, localsByName, diags)
	body = append(body, wasmir.Instruction{Kind: wasmir.InstrEnd})

	out.Funcs = append(out.Funcs, wasmir.Function{
		TypeIdx: typeIdx,
		Locals:  locals,
		Body:    body,
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

		switch pi.Name {
		case "local.get":
			if len(pi.Operands) != 1 {
				diags.Addf("func[%d]: local.get expects 1 operand", funcIdx)
				continue
			}

			localIndex, ok := lowerLocalIndexOperand(pi.Operands[0], localsByName)
			if !ok {
				diags.Addf("func[%d]: invalid local.get operand", funcIdx)
				continue
			}
			out = append(out, wasmir.Instruction{Kind: wasmir.InstrLocalGet, LocalIndex: localIndex})

		case "i32.add":
			if len(pi.Operands) != 0 {
				diags.Addf("func[%d]: i32.add expects no operands", funcIdx)
				continue
			}
			out = append(out, wasmir.Instruction{Kind: wasmir.InstrI32Add})

		default:
			diags.Addf("func[%d]: unsupported instruction %q", funcIdx, pi.Name)
		}
	}

	return out
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
	default:
		return 0, false
	}
}
