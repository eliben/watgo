package textformat

import "github.com/eliben/watgo/wasmir"

// Instruction is one text-format instruction node in the parser AST.
//
// The text AST preserves source syntax shape, so instructions may be either:
//   - PlainInstr: linear token form like "local.get 0" / "i32.add"
//   - FoldedInstr: folded S-expression form like "(i32.add (local.get 0) ...)"
//
// Lowering is the phase that normalizes both forms into canonical wasmir
// instructions.
type Instruction interface {
	isInstr()
	Loc() string
}

// PlainInstr represents one linear instruction in token sequence form.
// Examples:
//
//	local.get 0
//	call $f
//	i64.add
type PlainInstr struct {
	Name     string
	Operands []Operand
	loc      location
}

func (*PlainInstr) isInstr() {}

// Loc returns the source location of this instruction as "line:column".
// It returns an empty string when location is unavailable.
func (pi *PlainInstr) Loc() string {
	return pi.loc.String()
}

// FoldedArg is one argument in a folded instruction form "(op ...)".
// Exactly one of Operand or Instr is expected to be set.
type FoldedArg struct {
	Operand Operand
	Instr   Instruction
}

// Loc returns the source location of this folded argument.
func (fa FoldedArg) Loc() string {
	if fa.Operand != nil {
		return fa.Operand.Loc()
	}
	if fa.Instr != nil {
		return fa.Instr.Loc()
	}
	return ""
}

// FoldedInstr represents one folded instruction in S-expression form.
// Examples:
//
//	(i32.add (i32.const 1) (i32.const 2))
//	(if (result i64) (i64.eqz (local.get 0)) (then ...) (else ...))
//
// This is kept distinct from PlainInstr to preserve source-level syntax
// fidelity in the text AST.
type FoldedInstr struct {
	Name string
	Args []FoldedArg
	loc  location
}

func (*FoldedInstr) isInstr() {}

// Loc returns the source location of this instruction as "line:column".
// It returns an empty string when location is unavailable.
func (fi *FoldedInstr) Loc() string {
	return fi.loc.String()
}

// Operand is one operand in plain or folded instruction forms.
type Operand interface {
	isOperand()
	Loc() string
}

type IdOperand struct {
	Value string
	loc   location
}

func (*IdOperand) isOperand() {}

// Loc returns the source location of this operand as "line:column".
// It returns an empty string when location is unavailable.
func (op *IdOperand) Loc() string {
	return op.loc.String()
}

type IntOperand struct {
	Value string
	loc   location
}

func (*IntOperand) isOperand() {}

// Loc returns the source location of this operand as "line:column".
// It returns an empty string when location is unavailable.
func (op *IntOperand) Loc() string {
	return op.loc.String()
}

type FloatOperand struct {
	Value string
	loc   location
}

func (*FloatOperand) isOperand() {}

// Loc returns the source location of this operand as "line:column".
// It returns an empty string when location is unavailable.
func (op *FloatOperand) Loc() string {
	return op.loc.String()
}

type StringOperand struct {
	Value string
	loc   location
}

func (*StringOperand) isOperand() {}

// Loc returns the source location of this operand as "line:column".
// It returns an empty string when location is unavailable.
func (op *StringOperand) Loc() string {
	return op.loc.String()
}

type KeywordOperand struct {
	Value string
	loc   location
}

func (*KeywordOperand) isOperand() {}

// Loc returns the source location of this operand as "line:column".
// It returns an empty string when location is unavailable.
func (op *KeywordOperand) Loc() string {
	return op.loc.String()
}

// TypeOperand is a parsed value/reference type used as a plain structured
// control blocktype operand, for example in `if (result i32)`.
type TypeOperand struct {
	Ty  Type
	loc location
}

func (*TypeOperand) isOperand() {}

// Loc returns the source location of this operand as "line:column".
func (op *TypeOperand) Loc() string {
	return op.loc.String()
}

// instrSyntaxClass groups text-format instructions by their source-level
// parsing shape.
type instrSyntaxClass uint8

const (
	// instrSyntaxPlain covers plain token-sequence instructions whose operands
	// are parsed by the generic operand rules.
	instrSyntaxPlain instrSyntaxClass = iota

	// instrSyntaxMemory covers load/store instructions with memarg operands
	// such as align=/offset= and an optional memory index.
	instrSyntaxMemory

	// instrSyntaxStructured covers structured control instructions whose
	// surface syntax needs parser-side special handling, such as block/loop/if.
	instrSyntaxStructured

	// instrSyntaxSpecial covers instructions that need custom treatment outside
	// the generic plain-instruction path, for example call_indirect or
	// table.copy.
	instrSyntaxSpecial
)

// instrInfo is the shared instruction metadata stored in instructionInfoByName.
// It gives textformat code the semantic opcode plus the broad syntax family
// needed by parser and lowering decisions.
type instrInfo struct {
	kind        wasmir.InstrKind
	syntaxClass instrSyntaxClass
}

// instructionInfoByName is the single lookup table for instruction facts used
// across textformat. The key is the WAT opcode spelling, for example
// "i32.load" or "table.copy".
var instructionInfoByName map[string]instrInfo

func init() {
	instructionInfoByName = make(map[string]instrInfo)
	registerInstructions := func(class instrSyntaxClass, kinds map[string]wasmir.InstrKind) {
		for name, kind := range kinds {
			instructionInfoByName[name] = instrInfo{kind: kind, syntaxClass: class}
		}
	}

	registerInstructions(instrSyntaxStructured, map[string]wasmir.InstrKind{
		"block": wasmir.InstrBlock,
		"if":    wasmir.InstrIf,
		"loop":  wasmir.InstrLoop,
	})
	registerInstructions(instrSyntaxMemory, map[string]wasmir.InstrKind{
		"i32.load":          wasmir.InstrI32Load,
		"i64.load":          wasmir.InstrI64Load,
		"f32.load":          wasmir.InstrF32Load,
		"f64.load":          wasmir.InstrF64Load,
		"v128.load":         wasmir.InstrV128Load,
		"v128.load8x8_s":    wasmir.InstrV128Load8x8S,
		"v128.load8x8_u":    wasmir.InstrV128Load8x8U,
		"v128.load16x4_s":   wasmir.InstrV128Load16x4S,
		"v128.load16x4_u":   wasmir.InstrV128Load16x4U,
		"v128.load32x2_s":   wasmir.InstrV128Load32x2S,
		"v128.load32x2_u":   wasmir.InstrV128Load32x2U,
		"v128.load8_splat":  wasmir.InstrV128Load8Splat,
		"v128.load16_splat": wasmir.InstrV128Load16Splat,
		"v128.load32_splat": wasmir.InstrV128Load32Splat,
		"v128.load64_splat": wasmir.InstrV128Load64Splat,
		"i32.load8_s":       wasmir.InstrI32Load8S,
		"i32.load8_u":       wasmir.InstrI32Load8U,
		"i32.load16_s":      wasmir.InstrI32Load16S,
		"i32.load16_u":      wasmir.InstrI32Load16U,
		"i64.load8_s":       wasmir.InstrI64Load8S,
		"i64.load8_u":       wasmir.InstrI64Load8U,
		"i64.load16_s":      wasmir.InstrI64Load16S,
		"i64.load16_u":      wasmir.InstrI64Load16U,
		"i64.load32_s":      wasmir.InstrI64Load32S,
		"i64.load32_u":      wasmir.InstrI64Load32U,
		"i32.store":         wasmir.InstrI32Store,
		"i64.store":         wasmir.InstrI64Store,
		"i32.store8":        wasmir.InstrI32Store8,
		"i32.store16":       wasmir.InstrI32Store16,
		"i64.store8":        wasmir.InstrI64Store8,
		"i64.store16":       wasmir.InstrI64Store16,
		"i64.store32":       wasmir.InstrI64Store32,
		"f32.store":         wasmir.InstrF32Store,
		"f64.store":         wasmir.InstrF64Store,
		"v128.store":        wasmir.InstrV128Store,
	})
	registerInstructions(instrSyntaxSpecial, map[string]wasmir.InstrKind{
		"any.convert_extern": wasmir.InstrAnyConvertExtern,
		"array.get":          wasmir.InstrArrayGet,
		"array.get_s":        wasmir.InstrArrayGetS,
		"array.get_u":        wasmir.InstrArrayGetU,
		"array.len":          wasmir.InstrArrayLen,
		"array.new":          wasmir.InstrArrayNew,
		"array.new_data":     wasmir.InstrArrayNewData,
		"array.new_elem":     wasmir.InstrArrayNewElem,
		"array.new_default":  wasmir.InstrArrayNewDefault,
		"array.new_fixed":    wasmir.InstrArrayNewFixed,
		"array.init_data":    wasmir.InstrArrayInitData,
		"array.init_elem":    wasmir.InstrArrayInitElem,
		"array.fill":         wasmir.InstrArrayFill,
		"array.copy":         wasmir.InstrArrayCopy,
		"br_on_cast":         wasmir.InstrBrOnCast,
		"br_on_cast_fail":    wasmir.InstrBrOnCastFail,
		"extern.convert_any": wasmir.InstrExternConvertAny,
		"ref.eq":             wasmir.InstrRefEq,
		"array.set":          wasmir.InstrArraySet,
		"br_table":           wasmir.InstrBrTable,
		"call_indirect":      wasmir.InstrCallIndirect,
		"memory.copy":        wasmir.InstrMemoryCopy,
		"memory.fill":        wasmir.InstrMemoryFill,
		"memory.grow":        wasmir.InstrMemoryGrow,
		"memory.size":        wasmir.InstrMemorySize,
		"table.copy":         wasmir.InstrTableCopy,
		"table.fill":         wasmir.InstrTableFill,
		"table.get":          wasmir.InstrTableGet,
		"table.grow":         wasmir.InstrTableGrow,
		"table.init":         wasmir.InstrTableInit,
		"table.set":          wasmir.InstrTableSet,
		"table.size":         wasmir.InstrTableSize,
		"ref.cast":           wasmir.InstrRefCast,
		"ref.test":           wasmir.InstrRefTest,
		"struct.get":         wasmir.InstrStructGet,
		"struct.get_s":       wasmir.InstrStructGetS,
		"struct.get_u":       wasmir.InstrStructGetU,
		"struct.new":         wasmir.InstrStructNew,
		"struct.new_default": wasmir.InstrStructNewDefault,
		"struct.set":         wasmir.InstrStructSet,
	})
	registerInstructions(instrSyntaxPlain, map[string]wasmir.InstrKind{
		"br":                  wasmir.InstrBr,
		"br_if":               wasmir.InstrBrIf,
		"br_on_non_null":      wasmir.InstrBrOnNonNull,
		"br_on_null":          wasmir.InstrBrOnNull,
		"call":                wasmir.InstrCall,
		"call_ref":            wasmir.InstrCallRef,
		"data.drop":           wasmir.InstrDataDrop,
		"drop":                wasmir.InstrDrop,
		"elem.drop":           wasmir.InstrElemDrop,
		"else":                wasmir.InstrElse,
		"end":                 wasmir.InstrEnd,
		"f32.add":             wasmir.InstrF32Add,
		"f32.ceil":            wasmir.InstrF32Ceil,
		"f32.const":           wasmir.InstrF32Const,
		"f32.convert_i32_s":   wasmir.InstrF32ConvertI32S,
		"f32.div":             wasmir.InstrF32Div,
		"f32.eq":              wasmir.InstrF32Eq,
		"f32.floor":           wasmir.InstrF32Floor,
		"f32.gt":              wasmir.InstrF32Gt,
		"f32.lt":              wasmir.InstrF32Lt,
		"f32.max":             wasmir.InstrF32Max,
		"f32.min":             wasmir.InstrF32Min,
		"f32.mul":             wasmir.InstrF32Mul,
		"f32.ne":              wasmir.InstrF32Ne,
		"f32.nearest":         wasmir.InstrF32Nearest,
		"f32.neg":             wasmir.InstrF32Neg,
		"f32.reinterpret_i32": wasmir.InstrF32ReinterpretI32,
		"f32.sqrt":            wasmir.InstrF32Sqrt,
		"f32.sub":             wasmir.InstrF32Sub,
		"f32.trunc":           wasmir.InstrF32Trunc,
		"f64.add":             wasmir.InstrF64Add,
		"f64.ceil":            wasmir.InstrF64Ceil,
		"f64.const":           wasmir.InstrF64Const,
		"f64.convert_i64_s":   wasmir.InstrF64ConvertI64S,
		"f64.div":             wasmir.InstrF64Div,
		"f64.eq":              wasmir.InstrF64Eq,
		"f64.floor":           wasmir.InstrF64Floor,
		"f64.le":              wasmir.InstrF64Le,
		"f64.max":             wasmir.InstrF64Max,
		"f64.min":             wasmir.InstrF64Min,
		"f64.mul":             wasmir.InstrF64Mul,
		"f64.nearest":         wasmir.InstrF64Nearest,
		"f64.neg":             wasmir.InstrF64Neg,
		"f64.reinterpret_i64": wasmir.InstrF64ReinterpretI64,
		"f64.sqrt":            wasmir.InstrF64Sqrt,
		"f64.sub":             wasmir.InstrF64Sub,
		"f64.trunc":           wasmir.InstrF64Trunc,
		"global.get":          wasmir.InstrGlobalGet,
		"global.set":          wasmir.InstrGlobalSet,
		"i8x16.swizzle":       wasmir.InstrI8x16Swizzle,
		"i8x16.shl":           wasmir.InstrI8x16Shl,
		"i8x16.shr_s":         wasmir.InstrI8x16ShrS,
		"i8x16.shr_u":         wasmir.InstrI8x16ShrU,
		"i16x8.shl":           wasmir.InstrI16x8Shl,
		"i16x8.shr_s":         wasmir.InstrI16x8ShrS,
		"i16x8.shr_u":         wasmir.InstrI16x8ShrU,
		"i32x4.splat":         wasmir.InstrI32x4Splat,
		"i32x4.extract_lane":  wasmir.InstrI32x4ExtractLane,
		"i32x4.eq":            wasmir.InstrI32x4Eq,
		"i32x4.lt_s":          wasmir.InstrI32x4LtS,
		"i32x4.shl":           wasmir.InstrI32x4Shl,
		"i32x4.shr_s":         wasmir.InstrI32x4ShrS,
		"i32x4.shr_u":         wasmir.InstrI32x4ShrU,
		"i32x4.add":           wasmir.InstrI32x4Add,
		"i32x4.neg":           wasmir.InstrI32x4Neg,
		"i32x4.min_s":         wasmir.InstrI32x4MinS,
		"i64x2.shl":           wasmir.InstrI64x2Shl,
		"i64x2.shr_s":         wasmir.InstrI64x2ShrS,
		"i64x2.shr_u":         wasmir.InstrI64x2ShrU,
		"f32x4.add":           wasmir.InstrF32x4Add,
		"i32.add":             wasmir.InstrI32Add,
		"i32.and":             wasmir.InstrI32And,
		"i32.clz":             wasmir.InstrI32Clz,
		"i32.const":           wasmir.InstrI32Const,
		"i32.ctz":             wasmir.InstrI32Ctz,
		"i32.div_s":           wasmir.InstrI32DivS,
		"i32.div_u":           wasmir.InstrI32DivU,
		"i32.eq":              wasmir.InstrI32Eq,
		"i32.eqz":             wasmir.InstrI32Eqz,
		"i32.extend16_s":      wasmir.InstrI32Extend16S,
		"i32.extend8_s":       wasmir.InstrI32Extend8S,
		"i32.ge_s":            wasmir.InstrI32GeS,
		"i32.ge_u":            wasmir.InstrI32GeU,
		"i32.gt_s":            wasmir.InstrI32GtS,
		"i32.gt_u":            wasmir.InstrI32GtU,
		"i32.le_s":            wasmir.InstrI32LeS,
		"i32.le_u":            wasmir.InstrI32LeU,
		"i32.lt_s":            wasmir.InstrI32LtS,
		"i32.lt_u":            wasmir.InstrI32LtU,
		"i32.mul":             wasmir.InstrI32Mul,
		"i32.ne":              wasmir.InstrI32Ne,
		"i32.or":              wasmir.InstrI32Or,
		"i32.popcnt":          wasmir.InstrI32Popcnt,
		"i32.reinterpret_f32": wasmir.InstrI32ReinterpretF32,
		"i32.rem_s":           wasmir.InstrI32RemS,
		"i32.rem_u":           wasmir.InstrI32RemU,
		"i32.rotl":            wasmir.InstrI32Rotl,
		"i32.rotr":            wasmir.InstrI32Rotr,
		"i32.shl":             wasmir.InstrI32Shl,
		"i32.shr_s":           wasmir.InstrI32ShrS,
		"i32.shr_u":           wasmir.InstrI32ShrU,
		"i32.sub":             wasmir.InstrI32Sub,
		"i32.wrap_i64":        wasmir.InstrI32WrapI64,
		"i32.xor":             wasmir.InstrI32Xor,
		"i64.add":             wasmir.InstrI64Add,
		"i64.and":             wasmir.InstrI64And,
		"i64.clz":             wasmir.InstrI64Clz,
		"i64.const":           wasmir.InstrI64Const,
		"i64.ctz":             wasmir.InstrI64Ctz,
		"i64.div_s":           wasmir.InstrI64DivS,
		"i64.div_u":           wasmir.InstrI64DivU,
		"i64.eq":              wasmir.InstrI64Eq,
		"i64.eqz":             wasmir.InstrI64Eqz,
		"i64.extend16_s":      wasmir.InstrI64Extend16S,
		"i64.extend32_s":      wasmir.InstrI64Extend32S,
		"i64.extend8_s":       wasmir.InstrI64Extend8S,
		"i64.extend_i32_s":    wasmir.InstrI64ExtendI32S,
		"i64.extend_i32_u":    wasmir.InstrI64ExtendI32U,
		"i64.ge_s":            wasmir.InstrI64GeS,
		"i64.ge_u":            wasmir.InstrI64GeU,
		"i64.gt_s":            wasmir.InstrI64GtS,
		"i64.gt_u":            wasmir.InstrI64GtU,
		"i64.le_s":            wasmir.InstrI64LeS,
		"i64.le_u":            wasmir.InstrI64LeU,
		"i64.lt_s":            wasmir.InstrI64LtS,
		"i64.lt_u":            wasmir.InstrI64LtU,
		"i64.mul":             wasmir.InstrI64Mul,
		"i64.ne":              wasmir.InstrI64Ne,
		"i64.or":              wasmir.InstrI64Or,
		"i64.popcnt":          wasmir.InstrI64Popcnt,
		"i64.reinterpret_f64": wasmir.InstrI64ReinterpretF64,
		"i64.rem_s":           wasmir.InstrI64RemS,
		"i64.rem_u":           wasmir.InstrI64RemU,
		"i64.rotl":            wasmir.InstrI64Rotl,
		"i64.rotr":            wasmir.InstrI64Rotr,
		"i64.shl":             wasmir.InstrI64Shl,
		"i64.shr_s":           wasmir.InstrI64ShrS,
		"i64.shr_u":           wasmir.InstrI64ShrU,
		"i64.sub":             wasmir.InstrI64Sub,
		"i64.xor":             wasmir.InstrI64Xor,
		"local.get":           wasmir.InstrLocalGet,
		"local.set":           wasmir.InstrLocalSet,
		"local.tee":           wasmir.InstrLocalTee,
		"memory.init":         wasmir.InstrMemoryInit,
		"nop":                 wasmir.InstrNop,
		"ref.as_non_null":     wasmir.InstrRefAsNonNull,
		"ref.func":            wasmir.InstrRefFunc,
		"ref.i31":             wasmir.InstrRefI31,
		"ref.is_null":         wasmir.InstrRefIsNull,
		"ref.null":            wasmir.InstrRefNull,
		"i31.get_s":           wasmir.InstrI31GetS,
		"i31.get_u":           wasmir.InstrI31GetU,
		"return":              wasmir.InstrReturn,
		"select":              wasmir.InstrSelect,
		"unreachable":         wasmir.InstrUnreachable,
		"v128.const":          wasmir.InstrV128Const,
		"v128.bitselect":      wasmir.InstrV128Bitselect,
	})
}

// instructionKind looks up the semantic opcode for one text-format opcode.
func instructionKind(name string) (wasmir.InstrKind, bool) {
	entry, ok := instructionInfoByName[name]
	if !ok {
		return 0, false
	}
	return entry.kind, true
}

// instructionHasSyntaxClass reports whether name belongs to the given syntax
// family in the shared instruction catalog.
func instructionHasSyntaxClass(name string, class instrSyntaxClass) bool {
	entry, ok := instructionInfoByName[name]
	return ok && entry.syntaxClass == class
}
