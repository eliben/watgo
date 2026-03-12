package wasmir

// ValueType is a WebAssembly numeric value type.
type ValueType byte

const (
	ValueTypeI32 ValueType = iota
	ValueTypeI64
	ValueTypeF32
	ValueTypeF64
)

// InstrKind identifies one supported instruction opcode in semantic IR form.
type InstrKind uint8

const (
	InstrLocalGet InstrKind = iota
	InstrLocalSet
	InstrCall
	InstrBlock
	InstrLoop
	InstrIf
	InstrElse
	InstrBr
	InstrBrIf
	InstrUnreachable
	InstrReturn
	InstrI32Const
	InstrI64Const
	InstrF32Const
	InstrF64Const
	InstrDrop
	InstrI32Add
	InstrI32Sub
	InstrI32Mul
	InstrI32DivS
	InstrI32DivU
	InstrI32RemS
	InstrI32RemU
	InstrI32Shl
	InstrI32ShrS
	InstrI32ShrU
	InstrI32LtS
	InstrI32LtU
	InstrI64Add
	InstrI64Eq
	InstrI64Eqz
	InstrI64GtS
	InstrI64GtU
	InstrI64LeU
	InstrI64Sub
	InstrI64Mul
	InstrI64DivS
	InstrI64DivU
	InstrI64RemS
	InstrI64RemU
	InstrI64Shl
	InstrI64ShrS
	InstrI64ShrU
	InstrI64LtS
	InstrI64LtU
	InstrI32WrapI64
	InstrI64ExtendI32S
	InstrI64ExtendI32U
	InstrF32Add
	InstrF32Sub
	InstrF32Mul
	InstrF32Div
	InstrF32Sqrt
	InstrF32Min
	InstrF32Max
	InstrF32Ceil
	InstrF32Floor
	InstrF32Trunc
	InstrF32Nearest
	InstrF64Add
	InstrF64Sub
	InstrF64Mul
	InstrF64Div
	InstrF64Sqrt
	InstrF64Min
	InstrF64Max
	InstrF64Ceil
	InstrF64Floor
	InstrF64Trunc
	InstrF64Nearest
	InstrEnd
)

// ExternalKind identifies the kind of an exported external definition.
type ExternalKind uint8

const (
	ExternalKindFunction ExternalKind = iota
)

// Module is the semantic in-memory representation of a WebAssembly module.
//
// Index-based references are resolved through these slices:
//   - function type indices refer into Types
//   - function indices refer into Funcs
type Module struct {
	// Types is the module's function type table.
	Types []FuncType

	// Funcs is the list of function definitions in index order.
	Funcs []Function

	// Exports is the list of exported definitions.
	Exports []Export
}

// FuncType is a WebAssembly function signature.
type FuncType struct {
	// Params is the ordered parameter type list.

	Params []ValueType
	// Results is the ordered result type list.
	// For MVP this is typically length 0 or 1, but multi-value is representable.
	Results []ValueType
}

// Function is a function definition with locals and body instructions.
type Function struct {
	// TypeIdx indexes Module.Types and provides the function signature.
	TypeIdx uint32

	// Name is an optional source-level identifier (for diagnostics/debugging).
	Name string

	// ParamNames are optional source parameter identifiers aligned with
	// FuncType.Params. Empty entries mean the parameter had no identifier in
	// source.
	ParamNames []string

	// LocalNames are optional source local identifiers aligned with Locals.
	// Empty entries mean the local had no identifier in source.
	LocalNames []string

	// Locals is the ordered list of non-parameter local variable types.
	Locals []ValueType

	// Body is the function instruction stream.
	// Encoders/validators expect it to end with InstrEnd.
	Body []Instruction

	// SourceLoc is an optional source location string used in diagnostics.
	SourceLoc string
}

// Export is one module export entry.
type Export struct {
	// Name is the exported name visible to module users.
	Name string

	// Kind is the exported external kind.
	Kind ExternalKind

	// Index is the index into the corresponding module index space.
	// For ExternalKindFunction this indexes Module.Funcs.
	Index uint32
}

// Instruction is one semantic instruction.
//
// Kind selects which operand/immediate fields are meaningful. Fields not used
// by a given Kind are expected to be left at their zero value.
type Instruction struct {
	// Kind is the opcode of this instruction.
	Kind InstrKind

	// LocalIndex is the local index immediate used by InstrLocalGet.
	LocalIndex uint32

	// FuncIndex is the function index immediate used by InstrCall.
	FuncIndex uint32

	// BranchDepth is the label depth immediate used by InstrBr and InstrBrIf.
	BranchDepth uint32

	// BlockType is the if block result type for InstrIf when BlockHasResult is
	// true.
	BlockType ValueType

	// BlockHasResult reports whether InstrIf has an explicit result type.
	BlockHasResult bool

	// BlockTypeUsesIndex reports that structured control block type is encoded
	// as a type index into Module.Types (multi-value block signature).
	BlockTypeUsesIndex bool

	// BlockTypeIndex is the Module.Types index used when BlockTypeUsesIndex is
	// true.
	BlockTypeIndex uint32

	// I32Const is the immediate for InstrI32Const.
	I32Const int32

	// I64Const is the immediate for InstrI64Const.
	I64Const int64

	// F32Const is the raw IEEE-754 bits immediate for InstrF32Const.
	F32Const uint32

	// F64Const is the raw IEEE-754 bits immediate for InstrF64Const.
	F64Const uint64

	// SourceLoc is an optional source location string used in diagnostics.
	SourceLoc string
}
