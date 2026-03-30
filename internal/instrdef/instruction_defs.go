package instrdef

import . "github.com/eliben/watgo/wasmir"

// InstrSyntaxClass groups instructions by their source-level text syntax.
//
// Text-format parsing and lowering use this to distinguish plain token
// instructions from memory memarg forms, structured control syntax, and
// instructions that need custom parsing/lowering logic.
type InstrSyntaxClass uint8

const (
	InstrSyntaxPlain InstrSyntaxClass = iota
	InstrSyntaxMemory
	InstrSyntaxStructured
	InstrSyntaxSpecial
)

// LoweringOperandKind identifies which shared plain-instruction operand handler
// should be used by text lowering.
//
// A zero value means the instruction is lowered directly without processing any
// explicit operands.
type LoweringOperandKind uint8

const (
	LoweringOperandNone LoweringOperandKind = iota
	LoweringOperandLocalIndex
	LoweringOperandLocalSet
	LoweringOperandLocalTee
	LoweringOperandCall
	LoweringOperandCallRef
	LoweringOperandBranchDepth
	LoweringOperandGlobalIndex
	LoweringOperandGlobalSet
	LoweringOperandI32Const
	LoweringOperandI64Const
	LoweringOperandF32Const
	LoweringOperandF64Const
	LoweringOperandV128Const
	LoweringOperandLaneIndex
	LoweringOperandShuffleLanes
	LoweringOperandRefNull
	LoweringOperandRefFunc
	LoweringOperandDataIndex
	LoweringOperandElemIndex
)

// FixedStackSig describes the exact operand/result value types for a simple
// instruction that can be validated generically.
//
// It is intentionally limited to plain fixed-value signatures. Instructions
// whose validation depends on polymorphism, reference subtyping, control-flow
// context, or immediates use the handwritten validation path.
type FixedStackSig struct {
	Enabled     bool
	ParamCount  uint8
	Params      [3]ValueType
	ResultCount uint8
	Results     [3]ValueType
}

// InstructionTextDef contains the text-format facts for one instruction.
//
// Text-oriented consumers use this to recognize syntax families and generic
// plain-instruction lowering behavior.
type InstructionTextDef struct {
	SyntaxClass      InstrSyntaxClass
	OperandCount     int8
	LoweringOperands LoweringOperandKind
}

// BinaryEncodingKind describes how much of an instruction's binary encoding is
// covered by the shared instruction catalog.
type BinaryEncodingKind uint8

const (
	// BinaryEncodingNone means the instruction stays fully on handwritten
	// encode/decode paths.
	BinaryEncodingNone BinaryEncodingKind = iota

	// BinaryEncodingOpcodeOnly means the catalog provides the opcode or
	// prefixed subopcode, while immediates are still emitted/parsed by
	// handwritten code.
	BinaryEncodingOpcodeOnly

	// BinaryEncodingSimple means the instruction is fully handled by the
	// generic no-immediate encode/decode path.
	BinaryEncodingSimple
)

// InstructionBinaryDef contains generic binary encoding metadata for
// instructions that expose at least their opcode layout through the shared
// catalog.
type InstructionBinaryDef struct {
	Encoding BinaryEncodingKind
	Prefix   byte
	Opcode   uint32
}

// InstructionValidateDef contains generic validation metadata for
// instructions that can be checked through shared stack-signature logic.
type InstructionValidateDef struct {
	StackSig FixedStackSig
}

// InstructionDef centralizes the shared metadata for one semantic instruction.
//
// Not every instruction uses every facet. Consumers check the nested Text,
// Binary, and Validate metadata to decide whether the instruction can use a
// generic path or should stay on a handwritten one.
type InstructionDef struct {
	Kind     InstrKind
	TextName string
	Text     InstructionTextDef
	Binary   InstructionBinaryDef
	Validate InstructionValidateDef
}

type instructionBinaryKey struct {
	prefix byte
	opcode uint32
}

var instructionDefs = []InstructionDef{
	// Plain no-immediate instructions with generic binary and validation
	// support.
	directOp(InstrUnreachable, "unreachable", 0x00, noFixedSig()),
	directOp(InstrNop, "nop", 0x01, sigNoOp()),
	directOp(InstrElse, "else", 0x05, noFixedSig()),
	directOp(InstrEnd, "end", 0x0b, noFixedSig()),
	directOp(InstrReturn, "return", 0x0f, noFixedSig()),
	directOp(InstrDrop, "drop", 0x1a, noFixedSig()),
	directOp(InstrSelect, "select", 0x1b, noFixedSig()),

	directOp(InstrI32Eqz, "i32.eqz", 0x45, unarySig(ValueTypeI32, ValueTypeI32)),
	directOp(InstrI32Eq, "i32.eq", 0x46, binarySig(ValueTypeI32, ValueTypeI32, ValueTypeI32)),
	directOp(InstrI32Ne, "i32.ne", 0x47, binarySig(ValueTypeI32, ValueTypeI32, ValueTypeI32)),
	directOp(InstrI32LtS, "i32.lt_s", 0x48, binarySig(ValueTypeI32, ValueTypeI32, ValueTypeI32)),
	directOp(InstrI32LtU, "i32.lt_u", 0x49, binarySig(ValueTypeI32, ValueTypeI32, ValueTypeI32)),
	directOp(InstrI32GtS, "i32.gt_s", 0x4a, binarySig(ValueTypeI32, ValueTypeI32, ValueTypeI32)),
	directOp(InstrI32GtU, "i32.gt_u", 0x4b, binarySig(ValueTypeI32, ValueTypeI32, ValueTypeI32)),
	directOp(InstrI32LeS, "i32.le_s", 0x4c, binarySig(ValueTypeI32, ValueTypeI32, ValueTypeI32)),
	directOp(InstrI32LeU, "i32.le_u", 0x4d, binarySig(ValueTypeI32, ValueTypeI32, ValueTypeI32)),
	directOp(InstrI32GeS, "i32.ge_s", 0x4e, binarySig(ValueTypeI32, ValueTypeI32, ValueTypeI32)),
	directOp(InstrI32GeU, "i32.ge_u", 0x4f, binarySig(ValueTypeI32, ValueTypeI32, ValueTypeI32)),
	directOp(InstrI32Clz, "i32.clz", 0x67, unarySig(ValueTypeI32, ValueTypeI32)),
	directOp(InstrI32Ctz, "i32.ctz", 0x68, unarySig(ValueTypeI32, ValueTypeI32)),
	directOp(InstrI32Popcnt, "i32.popcnt", 0x69, unarySig(ValueTypeI32, ValueTypeI32)),
	directOp(InstrI32Add, "i32.add", 0x6a, binarySig(ValueTypeI32, ValueTypeI32, ValueTypeI32)),
	directOp(InstrI32Sub, "i32.sub", 0x6b, binarySig(ValueTypeI32, ValueTypeI32, ValueTypeI32)),
	directOp(InstrI32Mul, "i32.mul", 0x6c, binarySig(ValueTypeI32, ValueTypeI32, ValueTypeI32)),
	directOp(InstrI32DivS, "i32.div_s", 0x6d, binarySig(ValueTypeI32, ValueTypeI32, ValueTypeI32)),
	directOp(InstrI32DivU, "i32.div_u", 0x6e, binarySig(ValueTypeI32, ValueTypeI32, ValueTypeI32)),
	directOp(InstrI32RemS, "i32.rem_s", 0x6f, binarySig(ValueTypeI32, ValueTypeI32, ValueTypeI32)),
	directOp(InstrI32RemU, "i32.rem_u", 0x70, binarySig(ValueTypeI32, ValueTypeI32, ValueTypeI32)),
	directOp(InstrI32And, "i32.and", 0x71, binarySig(ValueTypeI32, ValueTypeI32, ValueTypeI32)),
	directOp(InstrI32Or, "i32.or", 0x72, binarySig(ValueTypeI32, ValueTypeI32, ValueTypeI32)),
	directOp(InstrI32Xor, "i32.xor", 0x73, binarySig(ValueTypeI32, ValueTypeI32, ValueTypeI32)),
	directOp(InstrI32Shl, "i32.shl", 0x74, binarySig(ValueTypeI32, ValueTypeI32, ValueTypeI32)),
	directOp(InstrI32ShrS, "i32.shr_s", 0x75, binarySig(ValueTypeI32, ValueTypeI32, ValueTypeI32)),
	directOp(InstrI32ShrU, "i32.shr_u", 0x76, binarySig(ValueTypeI32, ValueTypeI32, ValueTypeI32)),
	directOp(InstrI32Rotl, "i32.rotl", 0x77, binarySig(ValueTypeI32, ValueTypeI32, ValueTypeI32)),
	directOp(InstrI32Rotr, "i32.rotr", 0x78, binarySig(ValueTypeI32, ValueTypeI32, ValueTypeI32)),
	directOp(InstrI32Extend8S, "i32.extend8_s", 0xc0, unarySig(ValueTypeI32, ValueTypeI32)),
	directOp(InstrI32Extend16S, "i32.extend16_s", 0xc1, unarySig(ValueTypeI32, ValueTypeI32)),

	directOp(InstrI64Eqz, "i64.eqz", 0x50, unarySig(ValueTypeI64, ValueTypeI32)),
	directOp(InstrI64Eq, "i64.eq", 0x51, binarySig(ValueTypeI64, ValueTypeI64, ValueTypeI32)),
	directOp(InstrI64Ne, "i64.ne", 0x52, binarySig(ValueTypeI64, ValueTypeI64, ValueTypeI32)),
	directOp(InstrI64LtS, "i64.lt_s", 0x53, binarySig(ValueTypeI64, ValueTypeI64, ValueTypeI32)),
	directOp(InstrI64LtU, "i64.lt_u", 0x54, binarySig(ValueTypeI64, ValueTypeI64, ValueTypeI32)),
	directOp(InstrI64GtS, "i64.gt_s", 0x55, binarySig(ValueTypeI64, ValueTypeI64, ValueTypeI32)),
	directOp(InstrI64GtU, "i64.gt_u", 0x56, binarySig(ValueTypeI64, ValueTypeI64, ValueTypeI32)),
	directOp(InstrI64LeS, "i64.le_s", 0x57, binarySig(ValueTypeI64, ValueTypeI64, ValueTypeI32)),
	directOp(InstrI64LeU, "i64.le_u", 0x58, binarySig(ValueTypeI64, ValueTypeI64, ValueTypeI32)),
	directOp(InstrI64GeS, "i64.ge_s", 0x59, binarySig(ValueTypeI64, ValueTypeI64, ValueTypeI32)),
	directOp(InstrI64GeU, "i64.ge_u", 0x5a, binarySig(ValueTypeI64, ValueTypeI64, ValueTypeI32)),
	directOp(InstrI64Clz, "i64.clz", 0x79, unarySig(ValueTypeI64, ValueTypeI64)),
	directOp(InstrI64Ctz, "i64.ctz", 0x7a, unarySig(ValueTypeI64, ValueTypeI64)),
	directOp(InstrI64Popcnt, "i64.popcnt", 0x7b, unarySig(ValueTypeI64, ValueTypeI64)),
	directOp(InstrI64Add, "i64.add", 0x7c, binarySig(ValueTypeI64, ValueTypeI64, ValueTypeI64)),
	directOp(InstrI64Sub, "i64.sub", 0x7d, binarySig(ValueTypeI64, ValueTypeI64, ValueTypeI64)),
	directOp(InstrI64Mul, "i64.mul", 0x7e, binarySig(ValueTypeI64, ValueTypeI64, ValueTypeI64)),
	directOp(InstrI64DivS, "i64.div_s", 0x7f, binarySig(ValueTypeI64, ValueTypeI64, ValueTypeI64)),
	directOp(InstrI64DivU, "i64.div_u", 0x80, binarySig(ValueTypeI64, ValueTypeI64, ValueTypeI64)),
	directOp(InstrI64RemS, "i64.rem_s", 0x81, binarySig(ValueTypeI64, ValueTypeI64, ValueTypeI64)),
	directOp(InstrI64RemU, "i64.rem_u", 0x82, binarySig(ValueTypeI64, ValueTypeI64, ValueTypeI64)),
	directOp(InstrI64And, "i64.and", 0x83, binarySig(ValueTypeI64, ValueTypeI64, ValueTypeI64)),
	directOp(InstrI64Or, "i64.or", 0x84, binarySig(ValueTypeI64, ValueTypeI64, ValueTypeI64)),
	directOp(InstrI64Xor, "i64.xor", 0x85, binarySig(ValueTypeI64, ValueTypeI64, ValueTypeI64)),
	directOp(InstrI64Shl, "i64.shl", 0x86, binarySig(ValueTypeI64, ValueTypeI64, ValueTypeI64)),
	directOp(InstrI64ShrS, "i64.shr_s", 0x87, binarySig(ValueTypeI64, ValueTypeI64, ValueTypeI64)),
	directOp(InstrI64ShrU, "i64.shr_u", 0x88, binarySig(ValueTypeI64, ValueTypeI64, ValueTypeI64)),
	directOp(InstrI64Rotl, "i64.rotl", 0x89, binarySig(ValueTypeI64, ValueTypeI64, ValueTypeI64)),
	directOp(InstrI64Rotr, "i64.rotr", 0x8a, binarySig(ValueTypeI64, ValueTypeI64, ValueTypeI64)),
	directOp(InstrI64Extend8S, "i64.extend8_s", 0xc2, unarySig(ValueTypeI64, ValueTypeI64)),
	directOp(InstrI64Extend16S, "i64.extend16_s", 0xc3, unarySig(ValueTypeI64, ValueTypeI64)),
	directOp(InstrI64Extend32S, "i64.extend32_s", 0xc4, unarySig(ValueTypeI64, ValueTypeI64)),
	directOp(InstrI32WrapI64, "i32.wrap_i64", 0xa7, unarySig(ValueTypeI64, ValueTypeI32)),
	directOp(InstrI64ExtendI32S, "i64.extend_i32_s", 0xac, unarySig(ValueTypeI32, ValueTypeI64)),
	directOp(InstrI64ExtendI32U, "i64.extend_i32_u", 0xad, unarySig(ValueTypeI32, ValueTypeI64)),

	directOp(InstrF32Eq, "f32.eq", 0x5b, binarySig(ValueTypeF32, ValueTypeF32, ValueTypeI32)),
	directOp(InstrF32Ne, "f32.ne", 0x5c, binarySig(ValueTypeF32, ValueTypeF32, ValueTypeI32)),
	directOp(InstrF32Lt, "f32.lt", 0x5d, binarySig(ValueTypeF32, ValueTypeF32, ValueTypeI32)),
	directOp(InstrF32Gt, "f32.gt", 0x5e, binarySig(ValueTypeF32, ValueTypeF32, ValueTypeI32)),
	directOp(InstrF32Neg, "f32.neg", 0x8c, unarySig(ValueTypeF32, ValueTypeF32)),
	directOp(InstrF32Ceil, "f32.ceil", 0x8d, unarySig(ValueTypeF32, ValueTypeF32)),
	directOp(InstrF32Floor, "f32.floor", 0x8e, unarySig(ValueTypeF32, ValueTypeF32)),
	directOp(InstrF32Trunc, "f32.trunc", 0x8f, unarySig(ValueTypeF32, ValueTypeF32)),
	directOp(InstrF32Nearest, "f32.nearest", 0x90, unarySig(ValueTypeF32, ValueTypeF32)),
	directOp(InstrF32Sqrt, "f32.sqrt", 0x91, unarySig(ValueTypeF32, ValueTypeF32)),
	directOp(InstrF32Add, "f32.add", 0x92, binarySig(ValueTypeF32, ValueTypeF32, ValueTypeF32)),
	directOp(InstrF32Sub, "f32.sub", 0x93, binarySig(ValueTypeF32, ValueTypeF32, ValueTypeF32)),
	directOp(InstrF32Mul, "f32.mul", 0x94, binarySig(ValueTypeF32, ValueTypeF32, ValueTypeF32)),
	directOp(InstrF32Div, "f32.div", 0x95, binarySig(ValueTypeF32, ValueTypeF32, ValueTypeF32)),
	directOp(InstrF32Min, "f32.min", 0x96, binarySig(ValueTypeF32, ValueTypeF32, ValueTypeF32)),
	directOp(InstrF32Max, "f32.max", 0x97, binarySig(ValueTypeF32, ValueTypeF32, ValueTypeF32)),
	directOp(InstrF32ConvertI32S, "f32.convert_i32_s", 0xb2, unarySig(ValueTypeI32, ValueTypeF32)),

	directOp(InstrF64Eq, "f64.eq", 0x61, binarySig(ValueTypeF64, ValueTypeF64, ValueTypeI32)),
	directOp(InstrF64Le, "f64.le", 0x65, binarySig(ValueTypeF64, ValueTypeF64, ValueTypeI32)),
	directOp(InstrF64Neg, "f64.neg", 0x9a, unarySig(ValueTypeF64, ValueTypeF64)),
	directOp(InstrF64Ceil, "f64.ceil", 0x9b, unarySig(ValueTypeF64, ValueTypeF64)),
	directOp(InstrF64Floor, "f64.floor", 0x9c, unarySig(ValueTypeF64, ValueTypeF64)),
	directOp(InstrF64Trunc, "f64.trunc", 0x9d, unarySig(ValueTypeF64, ValueTypeF64)),
	directOp(InstrF64Nearest, "f64.nearest", 0x9e, unarySig(ValueTypeF64, ValueTypeF64)),
	directOp(InstrF64Sqrt, "f64.sqrt", 0x9f, unarySig(ValueTypeF64, ValueTypeF64)),
	directOp(InstrF64Add, "f64.add", 0xa0, binarySig(ValueTypeF64, ValueTypeF64, ValueTypeF64)),
	directOp(InstrF64Sub, "f64.sub", 0xa1, binarySig(ValueTypeF64, ValueTypeF64, ValueTypeF64)),
	directOp(InstrF64Mul, "f64.mul", 0xa2, binarySig(ValueTypeF64, ValueTypeF64, ValueTypeF64)),
	directOp(InstrF64Div, "f64.div", 0xa3, binarySig(ValueTypeF64, ValueTypeF64, ValueTypeF64)),
	directOp(InstrF64Min, "f64.min", 0xa4, binarySig(ValueTypeF64, ValueTypeF64, ValueTypeF64)),
	directOp(InstrF64Max, "f64.max", 0xa5, binarySig(ValueTypeF64, ValueTypeF64, ValueTypeF64)),
	directOp(InstrF64ConvertI64S, "f64.convert_i64_s", 0xb9, unarySig(ValueTypeI64, ValueTypeF64)),

	directOp(InstrI32ReinterpretF32, "i32.reinterpret_f32", 0xbc, unarySig(ValueTypeF32, ValueTypeI32)),
	directOp(InstrI64ReinterpretF64, "i64.reinterpret_f64", 0xbd, unarySig(ValueTypeF64, ValueTypeI64)),
	directOp(InstrF32ReinterpretI32, "f32.reinterpret_i32", 0xbe, unarySig(ValueTypeI32, ValueTypeF32)),
	directOp(InstrF64ReinterpretI64, "f64.reinterpret_i64", 0xbf, unarySig(ValueTypeI64, ValueTypeF64)),

	directOp(InstrRefIsNull, "ref.is_null", 0xd1, noFixedSig()),
	directOp(InstrRefAsNonNull, "ref.as_non_null", 0xd4, noFixedSig()),
	directOp(InstrRefEq, "ref.eq", 0xd3, noFixedSig()),

	prefixedOp(InstrAnyConvertExtern, "any.convert_extern", 0xfb, 0x1a, noFixedSig()),
	prefixedOp(InstrExternConvertAny, "extern.convert_any", 0xfb, 0x1b, noFixedSig()),
	prefixedOp(InstrRefI31, "ref.i31", 0xfb, 0x1c, noFixedSig()),
	prefixedOp(InstrI31GetS, "i31.get_s", 0xfb, 0x1d, noFixedSig()),
	prefixedOp(InstrI31GetU, "i31.get_u", 0xfb, 0x1e, noFixedSig()),
	prefixedOp(InstrArrayLen, "array.len", 0xfb, 0x0f, noFixedSig()),

	prefixedOp(InstrV128AnyTrue, "v128.any_true", 0xfd, 0x53, unarySig(ValueTypeV128, ValueTypeI32)),
	prefixedOp(InstrV128Not, "v128.not", 0xfd, 0x4d, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrV128And, "v128.and", 0xfd, 0x4e, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrV128AndNot, "v128.andnot", 0xfd, 0x4f, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrV128Or, "v128.or", 0xfd, 0x50, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrV128Xor, "v128.xor", 0xfd, 0x51, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrV128Bitselect, "v128.bitselect", 0xfd, 0x52, ternarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI8x16Splat, "i8x16.splat", 0xfd, 0x0f, unarySig(ValueTypeI32, ValueTypeV128)),
	prefixedOp(InstrI8x16Swizzle, "i8x16.swizzle", 0xfd, 0x0e, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI8x16Eq, "i8x16.eq", 0xfd, 0x23, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI8x16Ne, "i8x16.ne", 0xfd, 0x24, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI8x16LtS, "i8x16.lt_s", 0xfd, 0x25, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI8x16LtU, "i8x16.lt_u", 0xfd, 0x26, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI8x16GtS, "i8x16.gt_s", 0xfd, 0x27, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI8x16GtU, "i8x16.gt_u", 0xfd, 0x28, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI8x16LeS, "i8x16.le_s", 0xfd, 0x29, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI8x16LeU, "i8x16.le_u", 0xfd, 0x2a, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI8x16GeS, "i8x16.ge_s", 0xfd, 0x2b, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI8x16GeU, "i8x16.ge_u", 0xfd, 0x2c, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI8x16Abs, "i8x16.abs", 0xfd, 0x60, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI8x16Popcnt, "i8x16.popcnt", 0xfd, 0x62, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI8x16Neg, "i8x16.neg", 0xfd, 0x61, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI8x16AllTrue, "i8x16.all_true", 0xfd, 0x63, unarySig(ValueTypeV128, ValueTypeI32)),
	prefixedOp(InstrI8x16Bitmask, "i8x16.bitmask", 0xfd, 0x64, unarySig(ValueTypeV128, ValueTypeI32)),
	prefixedOp(InstrI8x16NarrowI16x8S, "i8x16.narrow_i16x8_s", 0xfd, 0x65, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI8x16NarrowI16x8U, "i8x16.narrow_i16x8_u", 0xfd, 0x66, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI8x16Shl, "i8x16.shl", 0xfd, 0x6b, binarySig(ValueTypeV128, ValueTypeI32, ValueTypeV128)),
	prefixedOp(InstrI8x16ShrS, "i8x16.shr_s", 0xfd, 0x6c, binarySig(ValueTypeV128, ValueTypeI32, ValueTypeV128)),
	prefixedOp(InstrI8x16ShrU, "i8x16.shr_u", 0xfd, 0x6d, binarySig(ValueTypeV128, ValueTypeI32, ValueTypeV128)),
	prefixedOp(InstrI8x16Add, "i8x16.add", 0xfd, 0x6e, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI8x16AddSatS, "i8x16.add_sat_s", 0xfd, 0x6f, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI8x16AddSatU, "i8x16.add_sat_u", 0xfd, 0x70, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI8x16Sub, "i8x16.sub", 0xfd, 0x71, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI8x16SubSatS, "i8x16.sub_sat_s", 0xfd, 0x72, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI8x16SubSatU, "i8x16.sub_sat_u", 0xfd, 0x73, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI8x16MinS, "i8x16.min_s", 0xfd, 0x76, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI8x16MinU, "i8x16.min_u", 0xfd, 0x77, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI8x16MaxS, "i8x16.max_s", 0xfd, 0x78, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI8x16MaxU, "i8x16.max_u", 0xfd, 0x79, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI8x16AvgrU, "i8x16.avgr_u", 0xfd, 0x7b, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),

	prefixedOp(InstrI16x8Eq, "i16x8.eq", 0xfd, 0x2d, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI16x8Ne, "i16x8.ne", 0xfd, 0x2e, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI16x8LtS, "i16x8.lt_s", 0xfd, 0x2f, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI16x8LtU, "i16x8.lt_u", 0xfd, 0x30, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI16x8GtS, "i16x8.gt_s", 0xfd, 0x31, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI16x8GtU, "i16x8.gt_u", 0xfd, 0x32, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI16x8LeS, "i16x8.le_s", 0xfd, 0x33, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI16x8LeU, "i16x8.le_u", 0xfd, 0x34, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI16x8GeS, "i16x8.ge_s", 0xfd, 0x35, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI16x8GeU, "i16x8.ge_u", 0xfd, 0x36, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI16x8ExtaddPairwiseI8x16S, "i16x8.extadd_pairwise_i8x16_s", 0xfd, 0x7c, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI16x8ExtaddPairwiseI8x16U, "i16x8.extadd_pairwise_i8x16_u", 0xfd, 0x7d, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI16x8Abs, "i16x8.abs", 0xfd, 0x80, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI16x8Neg, "i16x8.neg", 0xfd, 0x81, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI16x8Q15mulrSatS, "i16x8.q15mulr_sat_s", 0xfd, 0x82, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI16x8AllTrue, "i16x8.all_true", 0xfd, 0x83, unarySig(ValueTypeV128, ValueTypeI32)),
	prefixedOp(InstrI16x8Bitmask, "i16x8.bitmask", 0xfd, 0x84, unarySig(ValueTypeV128, ValueTypeI32)),
	prefixedOp(InstrI16x8NarrowI32x4S, "i16x8.narrow_i32x4_s", 0xfd, 0x85, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI16x8NarrowI32x4U, "i16x8.narrow_i32x4_u", 0xfd, 0x86, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI16x8ExtendLowI8x16S, "i16x8.extend_low_i8x16_s", 0xfd, 0x87, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI16x8ExtendLowI8x16U, "i16x8.extend_low_i8x16_u", 0xfd, 0x89, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI16x8ExtendHighI8x16S, "i16x8.extend_high_i8x16_s", 0xfd, 0x88, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI16x8ExtendHighI8x16U, "i16x8.extend_high_i8x16_u", 0xfd, 0x8a, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI16x8Shl, "i16x8.shl", 0xfd, 0x8b, binarySig(ValueTypeV128, ValueTypeI32, ValueTypeV128)),
	prefixedOp(InstrI16x8ShrS, "i16x8.shr_s", 0xfd, 0x8c, binarySig(ValueTypeV128, ValueTypeI32, ValueTypeV128)),
	prefixedOp(InstrI16x8ShrU, "i16x8.shr_u", 0xfd, 0x8d, binarySig(ValueTypeV128, ValueTypeI32, ValueTypeV128)),
	prefixedOp(InstrI16x8Add, "i16x8.add", 0xfd, 0x8e, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI16x8AddSatS, "i16x8.add_sat_s", 0xfd, 0x8f, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI16x8AddSatU, "i16x8.add_sat_u", 0xfd, 0x90, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI16x8Sub, "i16x8.sub", 0xfd, 0x91, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI16x8SubSatS, "i16x8.sub_sat_s", 0xfd, 0x92, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI16x8SubSatU, "i16x8.sub_sat_u", 0xfd, 0x93, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI16x8Mul, "i16x8.mul", 0xfd, 0x95, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI16x8MinS, "i16x8.min_s", 0xfd, 0x96, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI16x8MinU, "i16x8.min_u", 0xfd, 0x97, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI16x8MaxS, "i16x8.max_s", 0xfd, 0x98, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI16x8MaxU, "i16x8.max_u", 0xfd, 0x99, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI16x8AvgrU, "i16x8.avgr_u", 0xfd, 0x9b, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI16x8ExtmulLowI8x16S, "i16x8.extmul_low_i8x16_s", 0xfd, 0x9c, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI16x8ExtmulHighI8x16S, "i16x8.extmul_high_i8x16_s", 0xfd, 0x9d, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI16x8ExtmulLowI8x16U, "i16x8.extmul_low_i8x16_u", 0xfd, 0x9e, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI16x8ExtmulHighI8x16U, "i16x8.extmul_high_i8x16_u", 0xfd, 0x9f, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI16x8Splat, "i16x8.splat", 0xfd, 0x10, unarySig(ValueTypeI32, ValueTypeV128)),

	prefixedOp(InstrI32x4Splat, "i32x4.splat", 0xfd, 0x11, unarySig(ValueTypeI32, ValueTypeV128)),
	prefixedOp(InstrI32x4AllTrue, "i32x4.all_true", 0xfd, 0xa3, unarySig(ValueTypeV128, ValueTypeI32)),
	prefixedOp(InstrI32x4Bitmask, "i32x4.bitmask", 0xfd, 0xa4, unarySig(ValueTypeV128, ValueTypeI32)),
	prefixedOp(InstrI32x4Eq, "i32x4.eq", 0xfd, 0x37, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI32x4Ne, "i32x4.ne", 0xfd, 0x38, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI32x4LtS, "i32x4.lt_s", 0xfd, 0x39, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI32x4LtU, "i32x4.lt_u", 0xfd, 0x3a, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI32x4GtS, "i32x4.gt_s", 0xfd, 0x3b, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI32x4GtU, "i32x4.gt_u", 0xfd, 0x3c, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI32x4LeS, "i32x4.le_s", 0xfd, 0x3d, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI32x4LeU, "i32x4.le_u", 0xfd, 0x3e, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI32x4GeS, "i32x4.ge_s", 0xfd, 0x3f, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI32x4GeU, "i32x4.ge_u", 0xfd, 0x40, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI32x4ExtaddPairwiseI16x8S, "i32x4.extadd_pairwise_i16x8_s", 0xfd, 0x7e, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI32x4ExtaddPairwiseI16x8U, "i32x4.extadd_pairwise_i16x8_u", 0xfd, 0x7f, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI32x4Abs, "i32x4.abs", 0xfd, 0xa0, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI32x4ExtendLowI16x8S, "i32x4.extend_low_i16x8_s", 0xfd, 0xa7, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI32x4ExtendLowI16x8U, "i32x4.extend_low_i16x8_u", 0xfd, 0xa9, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI32x4ExtendHighI16x8S, "i32x4.extend_high_i16x8_s", 0xfd, 0xa8, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI32x4ExtendHighI16x8U, "i32x4.extend_high_i16x8_u", 0xfd, 0xaa, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI32x4Shl, "i32x4.shl", 0xfd, 0xab, binarySig(ValueTypeV128, ValueTypeI32, ValueTypeV128)),
	prefixedOp(InstrI32x4ShrS, "i32x4.shr_s", 0xfd, 0xac, binarySig(ValueTypeV128, ValueTypeI32, ValueTypeV128)),
	prefixedOp(InstrI32x4ShrU, "i32x4.shr_u", 0xfd, 0xad, binarySig(ValueTypeV128, ValueTypeI32, ValueTypeV128)),
	prefixedOp(InstrI32x4Add, "i32x4.add", 0xfd, 0xae, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI32x4Sub, "i32x4.sub", 0xfd, 0xb1, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI32x4Mul, "i32x4.mul", 0xfd, 0xb5, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI32x4Neg, "i32x4.neg", 0xfd, 0xa1, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI32x4MinS, "i32x4.min_s", 0xfd, 0xb6, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI32x4MinU, "i32x4.min_u", 0xfd, 0xb7, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI32x4MaxS, "i32x4.max_s", 0xfd, 0xb8, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI32x4MaxU, "i32x4.max_u", 0xfd, 0xb9, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI32x4DotI16x8S, "i32x4.dot_i16x8_s", 0xfd, 0xba, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI32x4ExtmulLowI16x8S, "i32x4.extmul_low_i16x8_s", 0xfd, 0xbc, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI32x4ExtmulHighI16x8S, "i32x4.extmul_high_i16x8_s", 0xfd, 0xbd, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI32x4ExtmulLowI16x8U, "i32x4.extmul_low_i16x8_u", 0xfd, 0xbe, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI32x4ExtmulHighI16x8U, "i32x4.extmul_high_i16x8_u", 0xfd, 0xbf, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),

	prefixedOp(InstrI64x2Eq, "i64x2.eq", 0xfd, 0xd6, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI64x2Ne, "i64x2.ne", 0xfd, 0xd7, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI64x2LtS, "i64x2.lt_s", 0xfd, 0xd8, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI64x2GtS, "i64x2.gt_s", 0xfd, 0xd9, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI64x2LeS, "i64x2.le_s", 0xfd, 0xda, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI64x2GeS, "i64x2.ge_s", 0xfd, 0xdb, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI64x2Abs, "i64x2.abs", 0xfd, 0xc0, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI64x2Neg, "i64x2.neg", 0xfd, 0xc1, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI64x2ExtendLowI32x4S, "i64x2.extend_low_i32x4_s", 0xfd, 0xc7, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI64x2ExtendLowI32x4U, "i64x2.extend_low_i32x4_u", 0xfd, 0xc9, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI64x2ExtendHighI32x4S, "i64x2.extend_high_i32x4_s", 0xfd, 0xc8, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI64x2ExtendHighI32x4U, "i64x2.extend_high_i32x4_u", 0xfd, 0xca, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI64x2AllTrue, "i64x2.all_true", 0xfd, 0xc3, unarySig(ValueTypeV128, ValueTypeI32)),
	prefixedOp(InstrI64x2Bitmask, "i64x2.bitmask", 0xfd, 0xc4, unarySig(ValueTypeV128, ValueTypeI32)),
	prefixedOp(InstrI64x2Shl, "i64x2.shl", 0xfd, 0xcb, binarySig(ValueTypeV128, ValueTypeI32, ValueTypeV128)),
	prefixedOp(InstrI64x2ShrS, "i64x2.shr_s", 0xfd, 0xcc, binarySig(ValueTypeV128, ValueTypeI32, ValueTypeV128)),
	prefixedOp(InstrI64x2ShrU, "i64x2.shr_u", 0xfd, 0xcd, binarySig(ValueTypeV128, ValueTypeI32, ValueTypeV128)),
	prefixedOp(InstrI64x2Add, "i64x2.add", 0xfd, 0xce, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI64x2Sub, "i64x2.sub", 0xfd, 0xd1, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI64x2Mul, "i64x2.mul", 0xfd, 0xd5, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI64x2ExtmulLowI32x4S, "i64x2.extmul_low_i32x4_s", 0xfd, 0xdc, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI64x2ExtmulHighI32x4S, "i64x2.extmul_high_i32x4_s", 0xfd, 0xdd, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI64x2ExtmulLowI32x4U, "i64x2.extmul_low_i32x4_u", 0xfd, 0xde, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI64x2ExtmulHighI32x4U, "i64x2.extmul_high_i32x4_u", 0xfd, 0xdf, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI64x2Splat, "i64x2.splat", 0xfd, 0x12, unarySig(ValueTypeI64, ValueTypeV128)),
	prefixedOp(InstrF32x4Splat, "f32x4.splat", 0xfd, 0x13, unarySig(ValueTypeF32, ValueTypeV128)),

	prefixedOp(InstrF32x4Eq, "f32x4.eq", 0xfd, 0x41, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF32x4Ne, "f32x4.ne", 0xfd, 0x42, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF32x4Lt, "f32x4.lt", 0xfd, 0x43, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF32x4Gt, "f32x4.gt", 0xfd, 0x44, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF32x4Le, "f32x4.le", 0xfd, 0x45, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF32x4Ge, "f32x4.ge", 0xfd, 0x46, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF32x4Ceil, "f32x4.ceil", 0xfd, 0x67, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF32x4Floor, "f32x4.floor", 0xfd, 0x68, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF32x4Trunc, "f32x4.trunc", 0xfd, 0x69, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF32x4Nearest, "f32x4.nearest", 0xfd, 0x6a, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF32x4Abs, "f32x4.abs", 0xfd, 0xe0, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF32x4Neg, "f32x4.neg", 0xfd, 0xe1, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF32x4Sqrt, "f32x4.sqrt", 0xfd, 0xe3, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI32x4TruncSatF32x4S, "i32x4.trunc_sat_f32x4_s", 0xfd, 0xf8, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI32x4TruncSatF32x4U, "i32x4.trunc_sat_f32x4_u", 0xfd, 0xf9, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF32x4ConvertI32x4S, "f32x4.convert_i32x4_s", 0xfd, 0xfa, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF32x4ConvertI32x4U, "f32x4.convert_i32x4_u", 0xfd, 0xfb, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF32x4Add, "f32x4.add", 0xfd, 0xe4, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF32x4Sub, "f32x4.sub", 0xfd, 0xe5, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF32x4Mul, "f32x4.mul", 0xfd, 0xe6, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF32x4Div, "f32x4.div", 0xfd, 0xe7, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF32x4Min, "f32x4.min", 0xfd, 0xe8, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF32x4Max, "f32x4.max", 0xfd, 0xe9, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF32x4Pmin, "f32x4.pmin", 0xfd, 0xea, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF32x4Pmax, "f32x4.pmax", 0xfd, 0xeb, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),

	prefixedOp(InstrF64x2Eq, "f64x2.eq", 0xfd, 0x47, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF64x2Ne, "f64x2.ne", 0xfd, 0x48, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF64x2Lt, "f64x2.lt", 0xfd, 0x49, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF64x2Gt, "f64x2.gt", 0xfd, 0x4a, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF64x2Le, "f64x2.le", 0xfd, 0x4b, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF64x2Ge, "f64x2.ge", 0xfd, 0x4c, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF64x2Ceil, "f64x2.ceil", 0xfd, 0x74, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF64x2Floor, "f64x2.floor", 0xfd, 0x75, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF64x2Trunc, "f64x2.trunc", 0xfd, 0x7a, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF64x2Nearest, "f64x2.nearest", 0xfd, 0x94, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF64x2Abs, "f64x2.abs", 0xfd, 0xec, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF64x2Neg, "f64x2.neg", 0xfd, 0xed, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF64x2Sqrt, "f64x2.sqrt", 0xfd, 0xef, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF64x2Add, "f64x2.add", 0xfd, 0xf0, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF64x2Sub, "f64x2.sub", 0xfd, 0xf1, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF64x2Mul, "f64x2.mul", 0xfd, 0xf2, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF64x2Div, "f64x2.div", 0xfd, 0xf3, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF64x2Min, "f64x2.min", 0xfd, 0xf4, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF64x2Max, "f64x2.max", 0xfd, 0xf5, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF64x2Pmin, "f64x2.pmin", 0xfd, 0xf6, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF64x2Pmax, "f64x2.pmax", 0xfd, 0xf7, binarySig(ValueTypeV128, ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI32x4TruncSatF64x2SZero, "i32x4.trunc_sat_f64x2_s_zero", 0xfd, 0xfc, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrI32x4TruncSatF64x2UZero, "i32x4.trunc_sat_f64x2_u_zero", 0xfd, 0xfd, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF64x2ConvertLowI32x4S, "f64x2.convert_low_i32x4_s", 0xfd, 0xfe, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF64x2ConvertLowI32x4U, "f64x2.convert_low_i32x4_u", 0xfd, 0xff, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF32x4DemoteF64x2Zero, "f32x4.demote_f64x2_zero", 0xfd, 0x5e, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF64x2PromoteLowF32x4, "f64x2.promote_low_f32x4", 0xfd, 0x5f, unarySig(ValueTypeV128, ValueTypeV128)),
	prefixedOp(InstrF64x2Splat, "f64x2.splat", 0xfd, 0x14, unarySig(ValueTypeF64, ValueTypeV128)),

	// Structured text instructions.
	withBinaryOpcode(structuredInstr(InstrBlock, "block"), 0, 0x02),
	withBinaryOpcode(structuredInstr(InstrIf, "if"), 0, 0x04),
	withBinaryOpcode(structuredInstr(InstrLoop, "loop"), 0, 0x03),

	// Memory memarg instructions.
	withBinaryOpcode(memoryInstr(InstrI32Load, "i32.load"), 0, 0x28),
	withBinaryOpcode(memoryInstr(InstrI64Load, "i64.load"), 0, 0x29),
	withBinaryOpcode(memoryInstr(InstrF32Load, "f32.load"), 0, 0x2a),
	withBinaryOpcode(memoryInstr(InstrF64Load, "f64.load"), 0, 0x2b),
	withBinaryOpcode(memoryInstr(InstrV128Load, "v128.load"), 0xfd, 0x00),
	withBinaryOpcode(memoryInstr(InstrV128Load8x8S, "v128.load8x8_s"), 0xfd, 0x01),
	withBinaryOpcode(memoryInstr(InstrV128Load8x8U, "v128.load8x8_u"), 0xfd, 0x02),
	withBinaryOpcode(memoryInstr(InstrV128Load16x4S, "v128.load16x4_s"), 0xfd, 0x03),
	withBinaryOpcode(memoryInstr(InstrV128Load16x4U, "v128.load16x4_u"), 0xfd, 0x04),
	withBinaryOpcode(memoryInstr(InstrV128Load32x2S, "v128.load32x2_s"), 0xfd, 0x05),
	withBinaryOpcode(memoryInstr(InstrV128Load32x2U, "v128.load32x2_u"), 0xfd, 0x06),
	withBinaryOpcode(memoryInstr(InstrV128Load8Splat, "v128.load8_splat"), 0xfd, 0x07),
	withBinaryOpcode(memoryInstr(InstrV128Load16Splat, "v128.load16_splat"), 0xfd, 0x08),
	withBinaryOpcode(memoryInstr(InstrV128Load32Splat, "v128.load32_splat"), 0xfd, 0x09),
	withBinaryOpcode(memoryInstr(InstrV128Load64Splat, "v128.load64_splat"), 0xfd, 0x0a),
	withBinaryOpcode(memoryInstr(InstrV128Load32Zero, "v128.load32_zero"), 0xfd, 0x5c),
	withBinaryOpcode(memoryInstr(InstrV128Load64Zero, "v128.load64_zero"), 0xfd, 0x5d),
	withBinaryOpcode(memoryInstr(InstrV128Load8Lane, "v128.load8_lane"), 0xfd, 0x54),
	withBinaryOpcode(memoryInstr(InstrV128Load16Lane, "v128.load16_lane"), 0xfd, 0x55),
	withBinaryOpcode(memoryInstr(InstrV128Load32Lane, "v128.load32_lane"), 0xfd, 0x56),
	withBinaryOpcode(memoryInstr(InstrV128Load64Lane, "v128.load64_lane"), 0xfd, 0x57),
	withBinaryOpcode(memoryInstr(InstrV128Store8Lane, "v128.store8_lane"), 0xfd, 0x58),
	withBinaryOpcode(memoryInstr(InstrV128Store16Lane, "v128.store16_lane"), 0xfd, 0x59),
	withBinaryOpcode(memoryInstr(InstrV128Store32Lane, "v128.store32_lane"), 0xfd, 0x5a),
	withBinaryOpcode(memoryInstr(InstrV128Store64Lane, "v128.store64_lane"), 0xfd, 0x5b),
	withBinaryOpcode(memoryInstr(InstrI32Load8S, "i32.load8_s"), 0, 0x2c),
	withBinaryOpcode(memoryInstr(InstrI32Load8U, "i32.load8_u"), 0, 0x2d),
	withBinaryOpcode(memoryInstr(InstrI32Load16S, "i32.load16_s"), 0, 0x2e),
	withBinaryOpcode(memoryInstr(InstrI32Load16U, "i32.load16_u"), 0, 0x2f),
	withBinaryOpcode(memoryInstr(InstrI64Load8S, "i64.load8_s"), 0, 0x30),
	withBinaryOpcode(memoryInstr(InstrI64Load8U, "i64.load8_u"), 0, 0x31),
	withBinaryOpcode(memoryInstr(InstrI64Load16S, "i64.load16_s"), 0, 0x32),
	withBinaryOpcode(memoryInstr(InstrI64Load16U, "i64.load16_u"), 0, 0x33),
	withBinaryOpcode(memoryInstr(InstrI64Load32S, "i64.load32_s"), 0, 0x34),
	withBinaryOpcode(memoryInstr(InstrI64Load32U, "i64.load32_u"), 0, 0x35),
	withBinaryOpcode(memoryInstr(InstrI32Store, "i32.store"), 0, 0x36),
	withBinaryOpcode(memoryInstr(InstrI64Store, "i64.store"), 0, 0x37),
	withBinaryOpcode(memoryInstr(InstrI32Store8, "i32.store8"), 0, 0x3a),
	withBinaryOpcode(memoryInstr(InstrI32Store16, "i32.store16"), 0, 0x3b),
	withBinaryOpcode(memoryInstr(InstrI64Store8, "i64.store8"), 0, 0x3c),
	withBinaryOpcode(memoryInstr(InstrI64Store16, "i64.store16"), 0, 0x3d),
	withBinaryOpcode(memoryInstr(InstrI64Store32, "i64.store32"), 0, 0x3e),
	withBinaryOpcode(memoryInstr(InstrF32Store, "f32.store"), 0, 0x38),
	withBinaryOpcode(memoryInstr(InstrF64Store, "f64.store"), 0, 0x39),
	withBinaryOpcode(memoryInstr(InstrV128Store, "v128.store"), 0xfd, 0x0b),

	// Special text instructions with handwritten parsing/lowering and/or
	// binary handling.
	withBinaryOpcode(specialInstr(InstrArrayGet, "array.get"), 0xfb, 0x0b),
	withBinaryOpcode(specialInstr(InstrArrayGetS, "array.get_s"), 0xfb, 0x0c),
	withBinaryOpcode(specialInstr(InstrArrayGetU, "array.get_u"), 0xfb, 0x0d),
	withBinaryOpcode(specialInstr(InstrArrayNew, "array.new"), 0xfb, 0x06),
	withBinaryOpcode(specialInstr(InstrArrayNewData, "array.new_data"), 0xfb, 0x09),
	withBinaryOpcode(specialInstr(InstrArrayNewElem, "array.new_elem"), 0xfb, 0x0a),
	withBinaryOpcode(specialInstr(InstrArrayNewDefault, "array.new_default"), 0xfb, 0x07),
	withBinaryOpcode(specialInstr(InstrArrayNewFixed, "array.new_fixed"), 0xfb, 0x08),
	withBinaryOpcode(specialInstr(InstrArrayInitData, "array.init_data"), 0xfb, 0x12),
	withBinaryOpcode(specialInstr(InstrArrayInitElem, "array.init_elem"), 0xfb, 0x13),
	withBinaryOpcode(specialInstr(InstrArrayFill, "array.fill"), 0xfb, 0x10),
	withBinaryOpcode(specialInstr(InstrArrayCopy, "array.copy"), 0xfb, 0x11),
	withBinaryOpcode(specialInstr(InstrArraySet, "array.set"), 0xfb, 0x0e),
	withBinaryOpcode(specialInstr(InstrBrOnCast, "br_on_cast"), 0xfb, 0x18),
	withBinaryOpcode(specialInstr(InstrBrOnCastFail, "br_on_cast_fail"), 0xfb, 0x19),
	withBinaryOpcode(specialInstr(InstrBrTable, "br_table"), 0, 0x0e),
	withBinaryOpcode(specialInstr(InstrCallIndirect, "call_indirect"), 0, 0x11),
	withBinaryOpcode(specialInstr(InstrMemoryCopy, "memory.copy"), 0xfc, 0x0a),
	withBinaryOpcode(specialInstr(InstrMemoryFill, "memory.fill"), 0xfc, 0x0b),
	withBinaryOpcode(specialInstr(InstrMemoryGrow, "memory.grow"), 0, 0x40),
	withBinaryOpcode(specialInstr(InstrMemorySize, "memory.size"), 0, 0x3f),
	specialInstr(InstrRefCast, "ref.cast"),
	specialInstr(InstrRefTest, "ref.test"),
	withBinaryOpcode(specialInstr(InstrStructGet, "struct.get"), 0xfb, 0x02),
	withBinaryOpcode(specialInstr(InstrStructGetS, "struct.get_s"), 0xfb, 0x03),
	withBinaryOpcode(specialInstr(InstrStructGetU, "struct.get_u"), 0xfb, 0x04),
	withBinaryOpcode(specialInstr(InstrStructNew, "struct.new"), 0xfb, 0x00),
	withBinaryOpcode(specialInstr(InstrStructNewDefault, "struct.new_default"), 0xfb, 0x01),
	withBinaryOpcode(specialInstr(InstrStructSet, "struct.set"), 0xfb, 0x05),
	withBinaryOpcode(specialInstr(InstrTableCopy, "table.copy"), 0xfc, 0x0e),
	withBinaryOpcode(specialInstr(InstrTableFill, "table.fill"), 0xfc, 0x11),
	withBinaryOpcode(specialInstr(InstrTableGet, "table.get"), 0, 0x25),
	withBinaryOpcode(specialInstr(InstrTableGrow, "table.grow"), 0xfc, 0x0f),
	withBinaryOpcode(specialInstr(InstrTableInit, "table.init"), 0xfc, 0x0c),
	withBinaryOpcode(specialInstr(InstrTableSet, "table.set"), 0, 0x26),
	withBinaryOpcode(specialInstr(InstrTableSize, "table.size"), 0xfc, 0x10),

	// Plain instructions with shared lowering operand handling.
	withBinaryOpcode(plainOperandInstr(InstrBr, "br", 1, LoweringOperandBranchDepth), 0, 0x0c),
	withBinaryOpcode(plainOperandInstr(InstrBrIf, "br_if", 1, LoweringOperandBranchDepth), 0, 0x0d),
	withBinaryOpcode(plainOperandInstr(InstrBrOnNonNull, "br_on_non_null", 1, LoweringOperandBranchDepth), 0, 0xd6),
	withBinaryOpcode(plainOperandInstr(InstrBrOnNull, "br_on_null", 1, LoweringOperandBranchDepth), 0, 0xd5),
	withBinaryOpcode(plainOperandInstr(InstrCall, "call", 1, LoweringOperandCall), 0, 0x10),
	withBinaryOpcode(plainOperandInstr(InstrCallRef, "call_ref", 1, LoweringOperandCallRef), 0, 0x14),
	withBinaryOpcode(plainOperandInstr(InstrDataDrop, "data.drop", 1, LoweringOperandDataIndex), 0xfc, 0x09),
	withBinaryOpcode(plainOperandInstr(InstrElemDrop, "elem.drop", 1, LoweringOperandElemIndex), 0xfc, 0x0d),
	withBinaryOpcode(plainOperandInstr(InstrF32Const, "f32.const", 1, LoweringOperandF32Const), 0, 0x43),
	withBinaryOpcode(plainOperandInstr(InstrF64Const, "f64.const", 1, LoweringOperandF64Const), 0, 0x44),
	withBinaryOpcode(plainOperandInstr(InstrGlobalGet, "global.get", 1, LoweringOperandGlobalIndex), 0, 0x23),
	withBinaryOpcode(plainOperandInstr(InstrGlobalSet, "global.set", 1, LoweringOperandGlobalSet), 0, 0x24),
	withBinaryOpcode(plainOperandInstr(InstrI32Const, "i32.const", 1, LoweringOperandI32Const), 0, 0x41),
	withBinaryOpcode(plainOperandInstr(InstrI8x16ExtractLaneS, "i8x16.extract_lane_s", 1, LoweringOperandLaneIndex), 0xfd, 0x15),
	withBinaryOpcode(plainOperandInstr(InstrI8x16ExtractLaneU, "i8x16.extract_lane_u", 1, LoweringOperandLaneIndex), 0xfd, 0x16),
	withBinaryOpcode(plainOperandInstr(InstrI8x16ReplaceLane, "i8x16.replace_lane", 1, LoweringOperandLaneIndex), 0xfd, 0x17),
	withBinaryOpcode(plainOperandInstr(InstrI8x16Shuffle, "i8x16.shuffle", 16, LoweringOperandShuffleLanes), 0xfd, 0x0d),
	withBinaryOpcode(plainOperandInstr(InstrI16x8ExtractLaneS, "i16x8.extract_lane_s", 1, LoweringOperandLaneIndex), 0xfd, 0x18),
	withBinaryOpcode(plainOperandInstr(InstrI16x8ExtractLaneU, "i16x8.extract_lane_u", 1, LoweringOperandLaneIndex), 0xfd, 0x19),
	withBinaryOpcode(plainOperandInstr(InstrI16x8ReplaceLane, "i16x8.replace_lane", 1, LoweringOperandLaneIndex), 0xfd, 0x1a),
	withBinaryOpcode(plainOperandInstr(InstrI32x4ExtractLane, "i32x4.extract_lane", 1, LoweringOperandLaneIndex), 0xfd, 0x1b),
	withBinaryOpcode(plainOperandInstr(InstrI32x4ReplaceLane, "i32x4.replace_lane", 1, LoweringOperandLaneIndex), 0xfd, 0x1c),
	withBinaryOpcode(plainOperandInstr(InstrI64Const, "i64.const", 1, LoweringOperandI64Const), 0, 0x42),
	withBinaryOpcode(plainOperandInstr(InstrI64x2ExtractLane, "i64x2.extract_lane", 1, LoweringOperandLaneIndex), 0xfd, 0x1d),
	withBinaryOpcode(plainOperandInstr(InstrI64x2ReplaceLane, "i64x2.replace_lane", 1, LoweringOperandLaneIndex), 0xfd, 0x1e),
	withBinaryOpcode(plainOperandInstr(InstrLocalGet, "local.get", 1, LoweringOperandLocalIndex), 0, 0x20),
	withBinaryOpcode(plainOperandInstr(InstrLocalSet, "local.set", 1, LoweringOperandLocalSet), 0, 0x21),
	withBinaryOpcode(plainOperandInstr(InstrLocalTee, "local.tee", 1, LoweringOperandLocalTee), 0, 0x22),
	withBinaryOpcode(plainOperandInstr(InstrMemoryInit, "memory.init", 1, LoweringOperandDataIndex), 0xfc, 0x08),
	withBinaryOpcode(plainOperandInstr(InstrF32x4ExtractLane, "f32x4.extract_lane", 1, LoweringOperandLaneIndex), 0xfd, 0x1f),
	withBinaryOpcode(plainOperandInstr(InstrF32x4ReplaceLane, "f32x4.replace_lane", 1, LoweringOperandLaneIndex), 0xfd, 0x20),
	withBinaryOpcode(plainOperandInstr(InstrF64x2ExtractLane, "f64x2.extract_lane", 1, LoweringOperandLaneIndex), 0xfd, 0x21),
	withBinaryOpcode(plainOperandInstr(InstrF64x2ReplaceLane, "f64x2.replace_lane", 1, LoweringOperandLaneIndex), 0xfd, 0x22),
	withBinaryOpcode(plainOperandInstr(InstrRefFunc, "ref.func", 1, LoweringOperandRefFunc), 0, 0xd2),
	withBinaryOpcode(plainOperandInstr(InstrRefNull, "ref.null", 1, LoweringOperandRefNull), 0, 0xd0),
	withBinaryOpcode(plainOperandInstr(InstrV128Const, "v128.const", -1, LoweringOperandV128Const), 0xfd, 0x0c),
}

var instructionByKind map[InstrKind]InstructionDef
var instructionByName map[string]InstructionDef
var instructionByBinary map[instructionBinaryKey]InstructionDef

func init() {
	instructionByKind = make(map[InstrKind]InstructionDef, len(instructionDefs))
	instructionByName = make(map[string]InstructionDef, len(instructionDefs))
	instructionByBinary = make(map[instructionBinaryKey]InstructionDef, len(instructionDefs))
	for _, def := range instructionDefs {
		instructionByKind[def.Kind] = def
		instructionByName[def.TextName] = def
		if def.Binary.Encoding != BinaryEncodingNone {
			instructionByBinary[instructionBinaryKey{prefix: def.Binary.Prefix, opcode: def.Binary.Opcode}] = def
		}
	}
}

// InstructionDefs returns the shared instruction catalog. The returned slice
// must be treated as read-only.
func InstructionDefs() []InstructionDef {
	return instructionDefs
}

// LookupInstructionByKind returns the centralized metadata for kind.
func LookupInstructionByKind(kind InstrKind) (InstructionDef, bool) {
	def, ok := instructionByKind[kind]
	return def, ok
}

// LookupInstructionByName returns the centralized metadata for a WAT
// opcode spelling such as "i32.add".
func LookupInstructionByName(name string) (InstructionDef, bool) {
	def, ok := instructionByName[name]
	return def, ok
}

// LookupInstructionByBinary returns the centralized metadata for a
// direct opcode or prefixed subopcode.
func LookupInstructionByBinary(prefix byte, opcode uint32) (InstructionDef, bool) {
	def, ok := instructionByBinary[instructionBinaryKey{prefix: prefix, opcode: opcode}]
	return def, ok
}

// Helper functions for defining instruction signatures and metadata.

func noFixedSig() FixedStackSig {
	return FixedStackSig{}
}

func sigNoOp() FixedStackSig {
	return FixedStackSig{Enabled: true}
}

func unarySig(param, result ValueType) FixedStackSig {
	return FixedStackSig{
		Enabled:     true,
		ParamCount:  1,
		Params:      [3]ValueType{param},
		ResultCount: 1,
		Results:     [3]ValueType{result},
	}
}

func binarySig(param1, param2, result ValueType) FixedStackSig {
	return FixedStackSig{
		Enabled:     true,
		ParamCount:  2,
		Params:      [3]ValueType{param1, param2},
		ResultCount: 1,
		Results:     [3]ValueType{result},
	}
}

func ternarySig(param1, param2, param3, result ValueType) FixedStackSig {
	return FixedStackSig{
		Enabled:     true,
		ParamCount:  3,
		Params:      [3]ValueType{param1, param2, param3},
		ResultCount: 1,
		Results:     [3]ValueType{result},
	}
}

func directOp(kind InstrKind, name string, opcode byte, sig FixedStackSig) InstructionDef {
	return InstructionDef{
		Kind:     kind,
		TextName: name,
		Text: InstructionTextDef{
			SyntaxClass: InstrSyntaxPlain,
		},
		Binary: InstructionBinaryDef{
			Encoding: BinaryEncodingSimple,
			Opcode:   uint32(opcode),
		},
		Validate: InstructionValidateDef{
			StackSig: sig,
		},
	}
}

func prefixedOp(kind InstrKind, name string, prefix byte, opcode uint32, sig FixedStackSig) InstructionDef {
	return InstructionDef{
		Kind:     kind,
		TextName: name,
		Text: InstructionTextDef{
			SyntaxClass: InstrSyntaxPlain,
		},
		Binary: InstructionBinaryDef{
			Encoding: BinaryEncodingSimple,
			Prefix:   prefix,
			Opcode:   opcode,
		},
		Validate: InstructionValidateDef{
			StackSig: sig,
		},
	}
}

func structuredInstr(kind InstrKind, name string) InstructionDef {
	return InstructionDef{
		Kind:     kind,
		TextName: name,
		Text: InstructionTextDef{
			SyntaxClass: InstrSyntaxStructured,
		},
	}
}

func memoryInstr(kind InstrKind, name string) InstructionDef {
	return InstructionDef{
		Kind:     kind,
		TextName: name,
		Text: InstructionTextDef{
			SyntaxClass: InstrSyntaxMemory,
		},
	}
}

func specialInstr(kind InstrKind, name string) InstructionDef {
	return InstructionDef{
		Kind:     kind,
		TextName: name,
		Text: InstructionTextDef{
			SyntaxClass: InstrSyntaxSpecial,
		},
	}
}

func plainOperandInstr(kind InstrKind, name string, operandCount int8, operands LoweringOperandKind) InstructionDef {
	return InstructionDef{
		Kind:     kind,
		TextName: name,
		Text: InstructionTextDef{
			SyntaxClass:      InstrSyntaxPlain,
			OperandCount:     operandCount,
			LoweringOperands: operands,
		},
	}
}

// withBinaryOpcode annotates a catalog entry with an opcode/subopcode while
// leaving any immediates on the handwritten binary path.
func withBinaryOpcode(def InstructionDef, prefix byte, opcode uint32) InstructionDef {
	def.Binary = InstructionBinaryDef{
		Encoding: BinaryEncodingOpcodeOnly,
		Prefix:   prefix,
		Opcode:   opcode,
	}
	return def
}
