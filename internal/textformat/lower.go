package textformat

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/eliben/watgo/diag"
	"github.com/eliben/watgo/wasmir"
)

// moduleLowerer owns module-wide lowering state.
type moduleLowerer struct {
	// out is the semantic module being constructed during lowering. All
	// successfully lowered types, functions, and exports are appended here as
	// we walk the AST, even if other parts fail and diagnostics are collected.
	out *wasmir.Module

	// diags accumulates every lowering diagnostic discovered for the module.
	// Lowering keeps going after errors so callers get a complete error list in
	// one pass instead of failing at the first issue.
	diags diag.ErrorList
}

// functionLowerer owns state while lowering one function.
type functionLowerer struct {
	// mod points to the parent module-level lowering context.
	mod *moduleLowerer

	// funcIdx is this function's index in the source module's function list.
	funcIdx int

	// fn is the source text-format function AST currently being lowered.
	// Per-function methods read declarations and instructions from this node.
	fn *Function

	// params holds lowered parameter value types in declaration order.
	params []wasmir.ValueType

	// results holds lowered result value types in declaration order.
	results []wasmir.ValueType

	// locals holds lowered local variable value types (excluding params).
	locals []wasmir.ValueType

	// body stores lowered semantic instructions as they are produced.
	body []wasmir.Instruction

	// localsByName maps text local identifiers (for params and locals) to their
	// resolved local indices.
	localsByName map[string]uint32

	// nextLocalIndex tracks the next available local index while processing
	// params and locals.
	nextLocalIndex uint32
}

// LowerModule lowers astm (a parsed text-format module) into a semantic
// wasmir.Module.
// It returns the lowered module (possibly partial) and nil on success.
// On any failure, it returns diag.ErrorList.
func LowerModule(astm *Module) (*wasmir.Module, error) {
	if astm == nil {
		return nil, diag.Fromf("module is nil")
	}

	l := newModuleLowerer()
	l.lowerModule(astm)
	if l.diags.HasAny() {
		return l.out, l.diags
	}
	return l.out, nil
}

// newModuleLowerer creates a module lowerer with an empty output module.
func newModuleLowerer() *moduleLowerer {
	return &moduleLowerer{out: &wasmir.Module{}}
}

// lowerModule lowers all functions in astm into l.out and accumulates
// diagnostics in l.diags.
func (l *moduleLowerer) lowerModule(astm *Module) {
	for i, f := range astm.Funcs {
		if f == nil {
			l.diags.Addf("func[%d]: nil function", i)
			continue
		}
		l.lowerFunction(i, f)
	}
}

// lowerFunction lowers one text-format function f as function number funcIdx
// into the output module.
func (l *moduleLowerer) lowerFunction(funcIdx int, f *Function) {
	fl := newFunctionLowerer(l, funcIdx, f)
	fl.lower()
}

// newFunctionLowerer constructs a per-function lowering context.
func newFunctionLowerer(mod *moduleLowerer, funcIdx int, fn *Function) *functionLowerer {
	return &functionLowerer{
		mod:          mod,
		funcIdx:      funcIdx,
		fn:           fn,
		localsByName: map[string]uint32{},
		body:         make([]wasmir.Instruction, 0, len(fn.Instrs)+1),
	}
}

// lower lowers fl.fn into fl.mod.out and records any diagnostics.
func (fl *functionLowerer) lower() {
	fl.lowerTypeUse()
	fl.lowerLocals()

	typeIdx := uint32(len(fl.mod.out.Types))
	fl.mod.out.Types = append(fl.mod.out.Types, wasmir.FuncType{Params: fl.params, Results: fl.results})

	fl.lowerInstrs()
	fl.body = append(fl.body, wasmir.Instruction{Kind: wasmir.InstrEnd, SourceLoc: fl.fn.loc.String()})

	fl.mod.out.Funcs = append(fl.mod.out.Funcs, wasmir.Function{
		TypeIdx:   typeIdx,
		Locals:    fl.locals,
		Body:      fl.body,
		SourceLoc: fl.fn.loc.String(),
	})

	if fl.fn.Export != "" {
		fl.mod.out.Exports = append(fl.mod.out.Exports, wasmir.Export{
			Name:  fl.fn.Export,
			Kind:  wasmir.ExternalKindFunction,
			Index: uint32(len(fl.mod.out.Funcs) - 1),
		})
	}
}

// lowerTypeUse lowers params/results from fl.fn.TyUse.
func (fl *functionLowerer) lowerTypeUse() {
	if fl.fn.TyUse == nil {
		fl.diagf(fl.fn.loc.String(), "missing function type use")
		return
	}
	fl.lowerParams(fl.fn.TyUse.Params)
	fl.lowerResults(fl.fn.TyUse.Results)
}

// lowerParams lowers parameter declarations and updates the local index space.
func (fl *functionLowerer) lowerParams(params []*ParamDecl) {
	for _, pd := range params {
		if pd == nil {
			fl.diagf("", "nil param declaration")
			continue
		}
		vt, ok := lowerValueType(pd.Ty)
		if !ok {
			fl.diagf(pd.loc.String(), "unsupported param type %q", pd.Ty)
			continue
		}

		fl.params = append(fl.params, vt)
		if pd.Id != "" {
			if _, exists := fl.localsByName[pd.Id]; exists {
				fl.diagf(pd.loc.String(), "duplicate param id %q", pd.Id)
			} else {
				fl.localsByName[pd.Id] = fl.nextLocalIndex
			}
		}
		fl.nextLocalIndex++
	}
}

// lowerResults lowers result declarations.
func (fl *functionLowerer) lowerResults(results []*ResultDecl) {
	for _, rd := range results {
		if rd == nil {
			fl.diagf("", "nil result declaration")
			continue
		}
		vt, ok := lowerValueType(rd.Ty)
		if !ok {
			fl.diagf(rd.loc.String(), "unsupported result type %q", rd.Ty)
			continue
		}
		fl.results = append(fl.results, vt)
	}
}

// lowerLocals lowers local declarations and updates the local index space.
func (fl *functionLowerer) lowerLocals() {
	for _, ld := range fl.fn.Locals {
		if ld == nil {
			fl.diagf("", "nil local declaration")
			continue
		}
		vt, ok := lowerValueType(ld.Ty)
		if !ok {
			fl.diagf(ld.loc.String(), "unsupported local type %q", ld.Ty)
			continue
		}

		fl.locals = append(fl.locals, vt)
		if ld.Id != "" {
			if _, exists := fl.localsByName[ld.Id]; exists {
				fl.diagf(ld.loc.String(), "duplicate local id %q", ld.Id)
			} else {
				fl.localsByName[ld.Id] = fl.nextLocalIndex
			}
		}
		fl.nextLocalIndex++
	}
}

// lowerInstrs lowers all instructions in fl.fn into fl.body.
func (fl *functionLowerer) lowerInstrs() {
	for _, instr := range fl.fn.Instrs {
		pi, ok := instr.(*PlainInstr)
		if !ok {
			fl.diagf(instr.Loc(), "unsupported instruction type %T", instr)
			continue
		}
		fl.lowerPlainInstr(pi)
	}
}

// lowerPlainInstr lowers one plain instruction into fl.body.
func (fl *functionLowerer) lowerPlainInstr(pi *PlainInstr) {
	instrLoc := pi.Loc()

	switch pi.Name {
	case "local.get":
		if len(pi.Operands) != 1 {
			fl.diagf(instrLoc, "local.get expects 1 operand")
			return
		}
		localIndex, ok := lowerLocalIndexOperand(pi.Operands[0], fl.localsByName)
		if !ok {
			fl.diagf(pi.Operands[0].Loc(), "invalid local.get operand")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrLocalGet, LocalIndex: localIndex, SourceLoc: instrLoc})

	case "i32.const":
		if len(pi.Operands) != 1 {
			fl.diagf(instrLoc, "i32.const expects 1 operand")
			return
		}
		imm, ok := lowerI32ConstOperand(pi.Operands[0])
		if !ok {
			fl.diagf(pi.Operands[0].Loc(), "invalid i32.const operand")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrI32Const, I32Const: imm, SourceLoc: instrLoc})

	case "i64.const":
		if len(pi.Operands) != 1 {
			fl.diagf(instrLoc, "i64.const expects 1 operand")
			return
		}
		imm, ok := lowerI64ConstOperand(pi.Operands[0])
		if !ok {
			fl.diagf(pi.Operands[0].Loc(), "invalid i64.const operand")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrI64Const, I64Const: imm, SourceLoc: instrLoc})

	case "f32.const":
		if len(pi.Operands) != 1 {
			fl.diagf(instrLoc, "f32.const expects 1 operand")
			return
		}
		imm, ok := lowerF32ConstOperand(pi.Operands[0])
		if !ok {
			fl.diagf(pi.Operands[0].Loc(), "invalid f32.const operand")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrF32Const, F32Const: imm, SourceLoc: instrLoc})

	case "drop":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "drop expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrDrop, SourceLoc: instrLoc})

	case "i32.add":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "i32.add expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrI32Add, SourceLoc: instrLoc})

	case "i32.sub":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "i32.sub expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrI32Sub, SourceLoc: instrLoc})

	case "i32.mul":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "i32.mul expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrI32Mul, SourceLoc: instrLoc})

	case "i32.div_s":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "i32.div_s expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrI32DivS, SourceLoc: instrLoc})

	case "i32.div_u":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "i32.div_u expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrI32DivU, SourceLoc: instrLoc})

	case "i64.add":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "i64.add expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrI64Add, SourceLoc: instrLoc})

	case "i64.sub":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "i64.sub expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrI64Sub, SourceLoc: instrLoc})

	case "i64.mul":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "i64.mul expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrI64Mul, SourceLoc: instrLoc})

	case "i64.div_s":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "i64.div_s expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrI64DivS, SourceLoc: instrLoc})

	case "i64.div_u":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "i64.div_u expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrI64DivU, SourceLoc: instrLoc})

	case "f32.add":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "f32.add expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrF32Add, SourceLoc: instrLoc})

	case "f32.sub":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "f32.sub expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrF32Sub, SourceLoc: instrLoc})

	case "f32.mul":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "f32.mul expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrF32Mul, SourceLoc: instrLoc})

	case "f32.div":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "f32.div expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrF32Div, SourceLoc: instrLoc})

	case "f32.sqrt":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "f32.sqrt expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrF32Sqrt, SourceLoc: instrLoc})

	case "f32.min":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "f32.min expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrF32Min, SourceLoc: instrLoc})

	case "f32.max":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "f32.max expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrF32Max, SourceLoc: instrLoc})

	case "f32.ceil":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "f32.ceil expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrF32Ceil, SourceLoc: instrLoc})

	case "f32.floor":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "f32.floor expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrF32Floor, SourceLoc: instrLoc})

	case "f32.trunc":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "f32.trunc expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrF32Trunc, SourceLoc: instrLoc})

	case "f32.nearest":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "f32.nearest expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrF32Nearest, SourceLoc: instrLoc})

	default:
		fl.diagf(instrLoc, "unsupported instruction %q", pi.Name)
	}
}

// emitInstr appends one lowered instruction to the current function body.
func (fl *functionLowerer) emitInstr(instr wasmir.Instruction) {
	fl.body = append(fl.body, instr)
}

// diagf adds one lowering diagnostic for the current function.
func (fl *functionLowerer) diagf(loc string, format string, args ...any) {
	addLowerDiag(&fl.mod.diags, fl.funcIdx, fl.fn.Id, loc, format, args...)
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

// lowerF32ConstOperand resolves op as an f32.const immediate.
// It returns IEEE-754 f32 bits and true on success.
func lowerF32ConstOperand(op Operand) (uint32, bool) {
	switch o := op.(type) {
	case *FloatOperand:
		return parseF32LiteralBits(o.Value)
	case *IntOperand:
		return parseF32LiteralBits(o.Value)
	default:
		return 0, false
	}
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

// parseF32LiteralBits parses s as an f32 literal and returns IEEE-754 bits.
// It accepts decimal/hex float forms, and integer forms used with f32.const.
func parseF32LiteralBits(s string) (uint32, bool) {
	clean := strings.ReplaceAll(s, "_", "")
	if clean == "" {
		return 0, false
	}

	// Fast path: Go supports decimal floats and hex floats with p-exponent.
	if bits, ok := parseF32WithParseFloat(clean); ok {
		return bits, true
	}

	sign, mag := splitSign(clean)
	if strings.HasPrefix(mag, "0x") || strings.HasPrefix(mag, "0X") {
		// Hex float forms without explicit exponent are valid in WAT.
		if strings.Contains(mag, ".") && !strings.ContainsAny(mag, "pP") {
			withExp := clean + "p0"
			if bits, ok := parseF32WithParseFloat(withExp); ok {
				return bits, true
			}
		}

		// Hex integer form for f32.const (e.g. 0x0123...).
		if !strings.Contains(mag, ".") && !strings.ContainsAny(mag, "pP") {
			u, err := strconv.ParseUint(mag[2:], 16, 64)
			if err != nil {
				return 0, false
			}
			f := float32(sign * float64(u))
			return float32bits(f), true
		}
	}

	return 0, false
}

// parseF32WithParseFloat parses s with strconv.ParseFloat and returns f32 bits.
func parseF32WithParseFloat(s string) (uint32, bool) {
	f, err := strconv.ParseFloat(s, 32)
	if err != nil {
		return 0, false
	}
	return float32bits(float32(f)), true
}

// splitSign splits s into sign (+1/-1) and unsigned magnitude string.
func splitSign(s string) (float64, string) {
	if s == "" {
		return 1, s
	}
	switch s[0] {
	case '+':
		return 1, s[1:]
	case '-':
		return -1, s[1:]
	default:
		return 1, s
	}
}

// float32bits returns IEEE-754 bits for f.
func float32bits(f float32) uint32 {
	return math.Float32bits(f)
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
//	"func[...] at <loc>: <message>"
//
// Otherwise:
//
//	"func[...]: <message>"
//
// Function context always includes the numeric function index and includes the
// textual function identifier too when present.
func addLowerDiag(diags *diag.ErrorList, funcIdx int, funcName string, loc string, format string, args ...any) {
	fnCtx := formatFunctionContext(funcIdx, funcName)
	if loc != "" {
		diags.Addf("%s at %s: "+format, append([]any{fnCtx, loc}, args...)...)
		return
	}
	diags.Addf("%s: "+format, append([]any{fnCtx}, args...)...)
}

// formatFunctionContext formats a function diagnostic prefix using function
// index and, when available, the source function identifier.
func formatFunctionContext(funcIdx int, funcName string) string {
	if funcName == "" {
		return fmt.Sprintf("func[%d]", funcIdx)
	}
	return fmt.Sprintf("func[%d] %s", funcIdx, funcName)
}
