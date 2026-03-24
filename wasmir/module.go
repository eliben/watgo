package wasmir

import "fmt"

// ValueKind classifies the broad kind of a WebAssembly value type.
type ValueKind uint8

const (
	ValueKindInvalid ValueKind = iota
	ValueKindI32
	ValueKindI64
	ValueKindF32
	ValueKindF64
	ValueKindRef
)

// HeapKind classifies the heap type component of a reference type.
type HeapKind uint8

const (
	HeapKindInvalid HeapKind = iota
	HeapKindFunc
	HeapKindExtern
	HeapKindNone
	HeapKindNoExtern
	HeapKindNoFunc
	HeapKindAny
	HeapKindEq
	HeapKindI31
	HeapKindArray
	HeapKindStruct
	HeapKindTypeIndex
)

// HeapType describes the heap type referenced by a reference value type.
type HeapType struct {
	Kind      HeapKind
	TypeIndex uint32
}

// ValueType is a WebAssembly value type.
//
// Numeric value types use only Kind. Reference value types use Kind=ValueKindRef
// and carry nullability plus heap type information.
type ValueType struct {
	Kind     ValueKind
	Nullable bool
	HeapType HeapType
}

var (
	ValueTypeI32 = ValueType{Kind: ValueKindI32}
	ValueTypeI64 = ValueType{Kind: ValueKindI64}
	ValueTypeF32 = ValueType{Kind: ValueKindF32}
	ValueTypeF64 = ValueType{Kind: ValueKindF64}
)

// RefTypeFunc returns a function-reference value type with the requested
// nullability.
func RefTypeFunc(nullable bool) ValueType {
	return ValueType{Kind: ValueKindRef, Nullable: nullable, HeapType: HeapType{Kind: HeapKindFunc}}
}

// RefTypeExtern returns an extern-reference value type with the requested
// nullability.
func RefTypeExtern(nullable bool) ValueType {
	return ValueType{Kind: ValueKindRef, Nullable: nullable, HeapType: HeapType{Kind: HeapKindExtern}}
}

func RefTypeNone(nullable bool) ValueType {
	return ValueType{Kind: ValueKindRef, Nullable: nullable, HeapType: HeapType{Kind: HeapKindNone}}
}

func RefTypeNoExtern(nullable bool) ValueType {
	return ValueType{Kind: ValueKindRef, Nullable: nullable, HeapType: HeapType{Kind: HeapKindNoExtern}}
}

func RefTypeNoFunc(nullable bool) ValueType {
	return ValueType{Kind: ValueKindRef, Nullable: nullable, HeapType: HeapType{Kind: HeapKindNoFunc}}
}

func RefTypeAny(nullable bool) ValueType {
	return ValueType{Kind: ValueKindRef, Nullable: nullable, HeapType: HeapType{Kind: HeapKindAny}}
}

func RefTypeEq(nullable bool) ValueType {
	return ValueType{Kind: ValueKindRef, Nullable: nullable, HeapType: HeapType{Kind: HeapKindEq}}
}

func RefTypeI31(nullable bool) ValueType {
	return ValueType{Kind: ValueKindRef, Nullable: nullable, HeapType: HeapType{Kind: HeapKindI31}}
}

func RefTypeArray(nullable bool) ValueType {
	return ValueType{Kind: ValueKindRef, Nullable: nullable, HeapType: HeapType{Kind: HeapKindArray}}
}

func RefTypeStruct(nullable bool) ValueType {
	return ValueType{Kind: ValueKindRef, Nullable: nullable, HeapType: HeapType{Kind: HeapKindStruct}}
}

// RefTypeIndexed returns a typed function reference to the given type index
// with the requested nullability.
func RefTypeIndexed(typeIndex uint32, nullable bool) ValueType {
	return ValueType{
		Kind:     ValueKindRef,
		Nullable: nullable,
		HeapType: HeapType{Kind: HeapKindTypeIndex, TypeIndex: typeIndex},
	}
}

func (vt ValueType) IsRef() bool {
	return vt.Kind == ValueKindRef
}

func (vt ValueType) UsesTypeIndex() bool {
	return vt.IsRef() && vt.HeapType.Kind == HeapKindTypeIndex
}

func (vt ValueType) String() string {
	switch vt.Kind {
	case ValueKindI32:
		return "i32"
	case ValueKindI64:
		return "i64"
	case ValueKindF32:
		return "f32"
	case ValueKindF64:
		return "f64"
	case ValueKindRef:
		switch vt.HeapType.Kind {
		case HeapKindFunc:
			if vt.Nullable {
				return "funcref"
			}
			return "(ref func)"
		case HeapKindExtern:
			if vt.Nullable {
				return "externref"
			}
			return "(ref extern)"
		case HeapKindNone:
			if vt.Nullable {
				return "nullref"
			}
			return "(ref none)"
		case HeapKindNoExtern:
			if vt.Nullable {
				return "(ref null noextern)"
			}
			return "(ref noextern)"
		case HeapKindNoFunc:
			if vt.Nullable {
				return "(ref null nofunc)"
			}
			return "(ref nofunc)"
		case HeapKindAny:
			if vt.Nullable {
				return "anyref"
			}
			return "(ref any)"
		case HeapKindEq:
			if vt.Nullable {
				return "eqref"
			}
			return "(ref eq)"
		case HeapKindI31:
			if vt.Nullable {
				return "i31ref"
			}
			return "(ref i31)"
		case HeapKindArray:
			if vt.Nullable {
				return "(ref null array)"
			}
			return "(ref array)"
		case HeapKindStruct:
			if vt.Nullable {
				return "(ref null struct)"
			}
			return "(ref struct)"
		case HeapKindTypeIndex:
			if vt.Nullable {
				return fmt.Sprintf("(ref null type[%d])", vt.HeapType.TypeIndex)
			}
			return fmt.Sprintf("(ref type[%d])", vt.HeapType.TypeIndex)
		default:
			return fmt.Sprintf("ref(kind=%d nullable=%t)", vt.HeapType.Kind, vt.Nullable)
		}
	default:
		return fmt.Sprintf("value_type(kind=%d)", vt.Kind)
	}
}

// InstrKind identifies one supported instruction opcode in semantic IR form.
type InstrKind uint8

const (
	InstrLocalGet InstrKind = iota
	InstrLocalSet
	InstrLocalTee
	InstrCall
	InstrCallIndirect
	InstrCallRef
	InstrBlock
	InstrLoop
	InstrIf
	InstrElse
	InstrBr
	InstrBrIf
	InstrBrOnNull
	InstrBrOnNonNull
	InstrBrOnCast
	InstrBrOnCastFail
	InstrBrTable
	InstrNop
	InstrUnreachable
	InstrReturn
	InstrI32Const
	InstrI64Const
	InstrF32Const
	InstrF64Const
	InstrDrop
	InstrSelect
	InstrGlobalGet
	InstrGlobalSet
	InstrTableGet
	InstrTableSet
	InstrTableCopy
	InstrTableFill
	InstrTableInit
	InstrElemDrop
	InstrTableGrow
	InstrTableSize
	InstrStructNew
	InstrStructNewDefault
	InstrStructGet
	InstrStructGetS
	InstrArrayNew
	InstrArrayLen
	InstrArrayNewDefault
	InstrArrayNewData
	InstrArrayNewElem
	InstrArrayNewFixed
	InstrArrayInitData
	InstrArrayInitElem
	InstrArrayGet
	InstrArrayGetS
	InstrArrayGetU
	InstrArraySet
	InstrArrayFill
	InstrArrayCopy
	InstrRefEq
	InstrRefTest
	InstrRefCast
	InstrRefI31
	InstrI31GetS
	InstrI31GetU
	InstrExternConvertAny
	InstrAnyConvertExtern
	InstrI32Load
	InstrI32Store
	InstrI64Load
	InstrF32Load
	InstrF64Load
	InstrI32Load8S
	InstrI32Load8U
	InstrI32Load16S
	InstrI32Load16U
	InstrI64Load8S
	InstrI64Load8U
	InstrI64Load16S
	InstrI64Load16U
	InstrI64Load32S
	InstrI64Load32U
	InstrI64Store
	InstrI32Store8
	InstrI32Store16
	InstrI64Store8
	InstrI64Store16
	InstrI64Store32
	InstrF32Store
	InstrF64Store
	InstrMemorySize
	InstrMemoryGrow
	InstrMemoryCopy
	InstrMemoryInit
	InstrMemoryFill
	InstrDataDrop
	InstrRefNull
	InstrRefIsNull
	InstrRefAsNonNull
	InstrRefFunc
	InstrI32Add
	InstrI32Sub
	InstrI32Mul
	InstrI32Or
	InstrI32Xor
	InstrI32Eq
	InstrI32Ne
	InstrI32Clz
	InstrI32Ctz
	InstrI32Popcnt
	InstrI32DivS
	InstrI32DivU
	InstrI32RemS
	InstrI32RemU
	InstrI32Shl
	InstrI32ShrS
	InstrI32ShrU
	InstrI32Rotl
	InstrI32Rotr
	InstrI32Eqz
	InstrI32LtS
	InstrI32LtU
	InstrI32LeS
	InstrI32LeU
	InstrI32GtS
	InstrI32GtU
	InstrI32GeS
	InstrI32GeU
	InstrI32And
	InstrI32Extend8S
	InstrI32Extend16S
	InstrI64Add
	InstrI64And
	InstrI64Or
	InstrI64Xor
	InstrI64Eq
	InstrI64Ne
	InstrI64Eqz
	InstrI64GtS
	InstrI64GtU
	InstrI64GeS
	InstrI64GeU
	InstrI64LeS
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
	InstrI64Rotl
	InstrI64Rotr
	InstrI64LtS
	InstrI64LtU
	InstrI64Clz
	InstrI64Ctz
	InstrI64Popcnt
	InstrI64Extend8S
	InstrI64Extend16S
	InstrI64Extend32S
	InstrI32WrapI64
	InstrI64ExtendI32S
	InstrI64ExtendI32U
	InstrF32ConvertI32S
	InstrF64ConvertI64S
	InstrF32Add
	InstrF32Sub
	InstrF32Mul
	InstrF32Div
	InstrF32Sqrt
	InstrF32Neg
	InstrF32Eq
	InstrF32Lt
	InstrF32Gt
	InstrF32Ne
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
	InstrF64Neg
	InstrF64Min
	InstrF64Max
	InstrF64Ceil
	InstrF64Floor
	InstrF64Trunc
	InstrF64Nearest
	InstrF64Eq
	InstrF64Le
	InstrI32ReinterpretF32
	InstrI64ReinterpretF64
	InstrF32ReinterpretI32
	InstrF64ReinterpretI64
	InstrEnd
)

// ExternalKind identifies the kind of an exported external definition.
type ExternalKind uint8

const (
	ExternalKindFunction ExternalKind = iota
	ExternalKindTable
	ExternalKindMemory
	ExternalKindGlobal
)

// Module is the semantic in-memory representation of a WebAssembly module.
//
// Index-based references are resolved through these slices:
//   - function type indices refer into Types
//   - function indices refer into imported functions first (from Imports), then
//     function definitions in Funcs
type Module struct {
	// Types is the module's function type table.
	Types []FuncType

	// Imports is the module's import list.
	Imports []Import

	// Funcs is the list of function definitions in index order.
	Funcs []Function

	// Tables is the table definition list in index order.
	Tables []Table

	// Memories is the linear memory definition list in index order.
	Memories []Memory

	// Globals is the global definition list in index order.
	Globals []Global

	// Data is the list of module data segments.
	Data []DataSegment

	// Exports is the list of exported definitions.
	Exports []Export

	// Elements is the list of active element segments used to initialize tables.
	Elements []ElementSegment
}

// FuncType is a WebAssembly function signature.
type FuncType struct {
	// Name is the optional source-level type identifier (for example "$t").
	Name string

	// RecGroupSize is the number of entries in the recursive type group for the
	// first type in that group. It is zero for types not starting a rec group.
	RecGroupSize uint32

	// SubType reports that this entry must be encoded as a GC subtype wrapper
	// instead of the short composite-type form.
	SubType bool

	// Final reports that the subtype wrapper is final.
	Final bool

	// SuperTypes is the declared supertype index list for GC/function subtypes.
	SuperTypes []uint32

	// Kind classifies this type table entry.
	Kind TypeDefKind

	// Params is the ordered parameter type list for function types.
	Params []ValueType

	// Results is the ordered result type list for function types.
	// For MVP this is typically length 0 or 1, but multi-value is representable.
	Results []ValueType

	// Fields carries the struct fields for GC struct types.
	Fields []FieldType

	// ElemField carries the array element field for GC array types.
	ElemField FieldType
}

// TypeDefKind classifies the kind of entry stored in Module.Types.
type TypeDefKind uint8

const (
	TypeDefKindFunc TypeDefKind = iota
	TypeDefKindStruct
	TypeDefKindArray
)

// FieldType is one GC struct or array field type.
type FieldType struct {
	Name    string
	Type    ValueType
	Packed  PackedType
	Mutable bool
}

type PackedType uint8

const (
	PackedTypeNone PackedType = iota
	PackedTypeI8
	PackedTypeI16
)

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

// Import is one module import entry.
type Import struct {
	// Module is the import module name.
	Module string

	// Name is the import field name.
	Name string

	// Kind is the external kind of this import.
	Kind ExternalKind

	// TypeIdx is used when Kind==ExternalKindFunction.
	TypeIdx uint32

	// Table is used when Kind==ExternalKindTable.
	Table Table

	// Memory is used when Kind==ExternalKindMemory.
	Memory Memory

	// GlobalType and GlobalMutable are used when Kind==ExternalKindGlobal.
	GlobalType    ValueType
	GlobalMutable bool
}

// Table is one table definition.
type Table struct {
	// AddressType is the table index type, either i32 or i64.
	AddressType ValueType

	// Min is the minimum table size in elements.
	Min uint64

	// HasMax reports whether Max is present.
	HasMax bool

	// Max is the maximum table size in elements when HasMax is true.
	Max uint64

	// RefType is the table element reference type.
	RefType ValueType

	// Init is the repeated inline initializer const expression, when present.
	// The instruction slice is expected to leave exactly one reference value on
	// the const-expression stack.
	Init []Instruction

	// ImportModule is set when this table is imported.
	ImportModule string

	// ImportName is set when this table is imported.
	ImportName string
}

// Memory is one linear memory definition.
type Memory struct {
	// AddressType is the memory address type, either i32 or i64.
	AddressType ValueType

	// Min is the minimum memory size in 64KiB pages.
	Min uint64

	// HasMax reports whether Max is present.
	HasMax bool

	// Max is the maximum memory size in 64KiB pages when HasMax is true.
	Max uint64

	// ImportModule is set when this memory is imported.
	ImportModule string

	// ImportName is set when this memory is imported.
	ImportName string
}

// DataSegmentMode classifies a data segment as active or passive.
type DataSegmentMode uint8

const (
	DataSegmentModeActive DataSegmentMode = iota
	DataSegmentModePassive
)

// DataSegment is one linear-memory data segment.
type DataSegment struct {
	// Mode classifies the segment as active or passive.
	Mode DataSegmentMode

	// MemoryIndex is the target memory index for active segments.
	MemoryIndex uint32

	// OffsetType is the const type used by the active segment offset expr.
	OffsetType ValueType

	// OffsetI64 is the integer value used by the active segment offset expr.
	// For i32 offsets it is sign-extended from the original i32.const. It is
	// ignored for passive segments.
	OffsetI64 int64

	// Init is the raw byte payload copied into memory at instantiation.
	Init []byte
}

// Global is one global definition.
type Global struct {
	// Name is an optional source-level identifier (for diagnostics/debugging).
	Name string

	// Type is the value type stored in this global.
	Type ValueType

	// Mutable reports whether this global can be written by global.set.
	Mutable bool

	// ImportModule is set when this global is imported.
	ImportModule string

	// ImportName is set when this global is imported.
	ImportName string

	// Init is the initializer constant expression for this global.
	// The instruction slice is expected to leave exactly one result on the
	// const-expression stack.
	Init []Instruction
}

// ElementSegment is one active table element segment.
type ElementSegment struct {
	// Mode classifies the segment as active, passive, or declarative.
	Mode ElemSegmentMode

	// TableIndex is the target table index.
	TableIndex uint32

	// OffsetType is the const type used by the active segment offset expr.
	OffsetType ValueType

	// OffsetI64 is the integer value used by the active segment offset expr.
	// For i32 offsets it is sign-extended from the original i32.const.
	OffsetI64 int64

	// FuncIndices are function indices written into the table.
	FuncIndices []uint32

	// Exprs is the expression form payload for reference-type element segments.
	// Each entry is one constant-expression instruction sequence, not just a
	// single instruction. This is needed because an elem item in WAT may be
	// written as a folded instruction, but wasmir stores instructions only in
	// linear form.
	//
	// For example, tests/wasmspec-scripts/gc/array_init_elem.wast contains:
	//   (elem $e1 arrayref
	//     (item (array.new_default $arrref_mut (i32.const 1)))
	//     (item (array.new_default $arrref_mut (i32.const 2))))
	// Each folded `item` lowers to a separate const-expr entry like:
	//   i32.const 1
	//   array.new_default $arrref_mut
	//
	// This matches the spec's element-segment expression encoding, where each
	// elem item is a full const expr terminated by `end`.
	// See: https://webassembly.github.io/gc/core/binary/modules.html#binary-elem
	//
	// When non-empty, FuncIndices should be empty.
	Exprs [][]Instruction

	// RefType is the reference type for Exprs.
	RefType ValueType
}

// ElemSegmentMode classifies an element segment by initialization mode.
type ElemSegmentMode uint8

const (
	ElemSegmentModeActive ElemSegmentMode = iota
	ElemSegmentModePassive
	ElemSegmentModeDeclarative
)

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

	// RefType is the reference value type immediate used by InstrRefNull.
	RefType ValueType

	// SourceRefType is the source reference type immediate used by br_on_cast
	// and br_on_cast_fail.
	SourceRefType ValueType

	// CallTypeIndex is the type index immediate used by InstrCallIndirect.
	CallTypeIndex uint32

	// TypeIndex is the referenced GC or function type index immediate used by
	// aggregate instructions such as struct.new, struct.get, and array.get.
	TypeIndex uint32

	// SourceTypeIndex is the secondary type index immediate used by array.copy.
	SourceTypeIndex uint32

	// FieldIndex is the field index immediate used by struct.get and struct.set.
	FieldIndex uint32

	// FixedCount is the fixed element count immediate used by array.new_fixed.
	FixedCount uint32

	// TableIndex is the table index immediate used by InstrCallIndirect.
	TableIndex uint32

	// SourceTableIndex is the source table index immediate used by table.copy.
	SourceTableIndex uint32

	// BranchDepth is the label depth immediate used by InstrBr and InstrBrIf.
	BranchDepth uint32

	// BranchTable is the label depth table immediate used by InstrBrTable.
	BranchTable []uint32

	// BranchDefault is the default label depth immediate used by InstrBrTable.
	BranchDefault uint32

	// GlobalIndex is the global index immediate used by global.{get,set}.
	GlobalIndex uint32

	// MemoryAlign is the alignment immediate used by memory load/store ops.
	MemoryAlign uint32

	// MemoryOffset is the offset immediate used by memory load/store ops.
	MemoryOffset uint64

	// MemoryIndex is the memory index immediate used by memory.grow.
	MemoryIndex uint32

	// SourceMemoryIndex is the source memory index immediate used by
	// memory.copy.
	SourceMemoryIndex uint32

	// DataIndex is the data segment index immediate used by memory.init and
	// data.drop.
	DataIndex uint32

	// ElemIndex is the element segment index immediate used by table.init and
	// elem.drop.
	ElemIndex uint32

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
