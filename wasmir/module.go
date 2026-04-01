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
	ValueKindV128
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
	ValueTypeI32  = ValueType{Kind: ValueKindI32}
	ValueTypeI64  = ValueType{Kind: ValueKindI64}
	ValueTypeF32  = ValueType{Kind: ValueKindF32}
	ValueTypeF64  = ValueType{Kind: ValueKindF64}
	ValueTypeV128 = ValueType{Kind: ValueKindV128}
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
	case ValueKindV128:
		return "v128"
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
type InstrKind uint16

const (
	InstrLocalGet InstrKind = iota
	InstrLocalSet
	InstrLocalTee
	InstrCall
	InstrReturnCall
	InstrCallIndirect
	InstrReturnCallIndirect
	InstrCallRef
	InstrReturnCallRef
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
	InstrV128Const
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
	InstrStructGetU
	InstrStructSet
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
	InstrV128Load
	InstrV128Load8x8S
	InstrV128Load8x8U
	InstrV128Load16x4S
	InstrV128Load16x4U
	InstrV128Load32x2S
	InstrV128Load32x2U
	InstrV128Load8Splat
	InstrV128Load16Splat
	InstrV128Load32Splat
	InstrV128Load64Splat
	InstrV128Load32Zero
	InstrV128Load64Zero
	InstrV128Load8Lane
	InstrV128Load16Lane
	InstrV128Load32Lane
	InstrV128Load64Lane
	InstrV128Store8Lane
	InstrV128Store16Lane
	InstrV128Store32Lane
	InstrV128Store64Lane
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
	InstrV128Store
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
	InstrV128AnyTrue
	InstrV128Not
	InstrV128And
	InstrV128AndNot
	InstrV128Or
	InstrV128Xor
	InstrI8x16Splat
	InstrI8x16Shuffle
	InstrI8x16Swizzle
	InstrI8x16RelaxedSwizzle
	InstrI8x16ExtractLaneS
	InstrI8x16ExtractLaneU
	InstrI8x16ReplaceLane
	InstrI8x16Eq
	InstrI8x16Ne
	InstrI8x16LtS
	InstrI8x16LtU
	InstrI8x16GtS
	InstrI8x16GtU
	InstrI8x16LeS
	InstrI8x16LeU
	InstrI8x16GeS
	InstrI8x16GeU
	InstrI8x16Abs
	InstrI8x16Popcnt
	InstrI8x16Neg
	InstrI8x16AllTrue
	InstrI8x16Bitmask
	InstrI8x16NarrowI16x8S
	InstrI8x16NarrowI16x8U
	InstrI8x16Shl
	InstrI8x16ShrS
	InstrI8x16ShrU
	InstrI8x16Add
	InstrI8x16AddSatS
	InstrI8x16AddSatU
	InstrI8x16Sub
	InstrI8x16SubSatS
	InstrI8x16SubSatU
	InstrI8x16MinS
	InstrI8x16MinU
	InstrI8x16MaxS
	InstrI8x16MaxU
	InstrI8x16AvgrU
	InstrI16x8Eq
	InstrI16x8Ne
	InstrI16x8ExtractLaneS
	InstrI16x8ExtractLaneU
	InstrI16x8ReplaceLane
	InstrI16x8LtS
	InstrI16x8LtU
	InstrI16x8GtS
	InstrI16x8GtU
	InstrI16x8LeS
	InstrI16x8LeU
	InstrI16x8GeS
	InstrI16x8GeU
	InstrI16x8ExtaddPairwiseI8x16S
	InstrI16x8ExtaddPairwiseI8x16U
	InstrI16x8Abs
	InstrI16x8Neg
	InstrI16x8Q15mulrSatS
	InstrI16x8AllTrue
	InstrI16x8Bitmask
	InstrI16x8NarrowI32x4S
	InstrI16x8NarrowI32x4U
	InstrI16x8ExtendLowI8x16S
	InstrI16x8ExtendLowI8x16U
	InstrI16x8ExtendHighI8x16S
	InstrI16x8ExtendHighI8x16U
	InstrI16x8Shl
	InstrI16x8ShrS
	InstrI16x8ShrU
	InstrI16x8Add
	InstrI16x8AddSatS
	InstrI16x8AddSatU
	InstrI16x8Sub
	InstrI16x8SubSatS
	InstrI16x8SubSatU
	InstrI16x8Mul
	InstrI16x8MinS
	InstrI16x8MinU
	InstrI16x8MaxS
	InstrI16x8MaxU
	InstrI16x8AvgrU
	InstrI16x8RelaxedQ15mulrS
	InstrI16x8RelaxedDotI8x16I7x16S
	InstrI16x8ExtmulLowI8x16S
	InstrI16x8ExtmulHighI8x16S
	InstrI16x8ExtmulLowI8x16U
	InstrI16x8ExtmulHighI8x16U
	InstrI16x8Splat
	InstrI32x4Splat
	InstrI32x4ExtractLane
	InstrI32x4ReplaceLane
	InstrI32x4AllTrue
	InstrI32x4Bitmask
	InstrI32x4Eq
	InstrI32x4Ne
	InstrI32x4LtS
	InstrI32x4LtU
	InstrI32x4GtS
	InstrI32x4GtU
	InstrI32x4LeS
	InstrI32x4LeU
	InstrI32x4GeS
	InstrI32x4GeU
	InstrI32x4ExtaddPairwiseI16x8S
	InstrI32x4ExtaddPairwiseI16x8U
	InstrI32x4Abs
	InstrI32x4ExtendLowI16x8S
	InstrI32x4ExtendLowI16x8U
	InstrI32x4ExtendHighI16x8S
	InstrI32x4ExtendHighI16x8U
	InstrI32x4Shl
	InstrI32x4ShrS
	InstrI32x4ShrU
	InstrI32x4Add
	InstrI32x4Sub
	InstrI32x4Mul
	InstrI32x4Neg
	InstrI32x4MinS
	InstrI32x4MinU
	InstrI32x4MaxS
	InstrI32x4MaxU
	InstrI32x4DotI16x8S
	InstrI32x4RelaxedTruncF32x4S
	InstrI32x4RelaxedTruncF32x4U
	InstrI32x4RelaxedTruncF64x2SZero
	InstrI32x4RelaxedTruncF64x2UZero
	InstrI32x4RelaxedLaneselect
	InstrI32x4RelaxedDotI8x16I7x16AddS
	InstrI32x4ExtmulLowI16x8S
	InstrI32x4ExtmulHighI16x8S
	InstrI32x4ExtmulLowI16x8U
	InstrI32x4ExtmulHighI16x8U
	InstrI64x2Eq
	InstrI64x2Ne
	InstrI64x2LtS
	InstrI64x2GtS
	InstrI64x2LeS
	InstrI64x2GeS
	InstrI64x2Abs
	InstrI64x2Neg
	InstrI64x2ExtendLowI32x4S
	InstrI64x2ExtendLowI32x4U
	InstrI64x2ExtendHighI32x4S
	InstrI64x2ExtendHighI32x4U
	InstrI64x2AllTrue
	InstrI64x2Bitmask
	InstrI64x2Shl
	InstrI64x2ShrS
	InstrI64x2ShrU
	InstrI64x2Add
	InstrI64x2Sub
	InstrI64x2Mul
	InstrI64x2RelaxedLaneselect
	InstrI64x2ExtmulLowI32x4S
	InstrI64x2ExtmulHighI32x4S
	InstrI64x2ExtmulLowI32x4U
	InstrI64x2ExtmulHighI32x4U
	InstrI64x2Splat
	InstrF32x4Splat
	InstrI64x2ExtractLane
	InstrI64x2ReplaceLane
	InstrF32x4ExtractLane
	InstrF32x4ReplaceLane
	InstrF32x4Eq
	InstrF32x4Ne
	InstrF32x4Lt
	InstrF32x4Gt
	InstrF32x4Le
	InstrF32x4Ge
	InstrF32x4Ceil
	InstrF32x4Floor
	InstrF32x4Trunc
	InstrF32x4Nearest
	InstrF32x4Abs
	InstrF32x4Neg
	InstrF32x4Sqrt
	InstrI32x4TruncSatF32x4S
	InstrI32x4TruncSatF32x4U
	InstrF32x4ConvertI32x4S
	InstrF32x4ConvertI32x4U
	InstrF32x4Add
	InstrF32x4Sub
	InstrF32x4Mul
	InstrF32x4Div
	InstrF32x4Min
	InstrF32x4Max
	InstrF32x4Pmin
	InstrF32x4Pmax
	InstrF32x4RelaxedMadd
	InstrF32x4RelaxedNmadd
	InstrI8x16RelaxedLaneselect
	InstrF32x4RelaxedMin
	InstrF32x4RelaxedMax
	InstrF64x2Eq
	InstrF64x2Ne
	InstrF64x2Lt
	InstrF64x2Gt
	InstrF64x2Le
	InstrF64x2Ge
	InstrF64x2Ceil
	InstrF64x2Floor
	InstrF64x2Trunc
	InstrF64x2Nearest
	InstrF64x2Abs
	InstrF64x2Neg
	InstrF64x2Sqrt
	InstrF64x2Add
	InstrF64x2Sub
	InstrF64x2Mul
	InstrF64x2Div
	InstrF64x2Min
	InstrF64x2Max
	InstrF64x2Pmin
	InstrF64x2Pmax
	InstrF64x2RelaxedMadd
	InstrF64x2RelaxedNmadd
	InstrI16x8RelaxedLaneselect
	InstrF64x2RelaxedMin
	InstrF64x2RelaxedMax
	InstrI32x4TruncSatF64x2SZero
	InstrI32x4TruncSatF64x2UZero
	InstrF64x2ConvertLowI32x4S
	InstrF64x2ConvertLowI32x4U
	InstrF32x4DemoteF64x2Zero
	InstrF64x2PromoteLowF32x4
	InstrF64x2Splat
	InstrF64x2ExtractLane
	InstrF64x2ReplaceLane
	InstrV128Bitselect
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
	InstrI32TruncF32S
	InstrI32TruncF32U
	InstrI32TruncF64S
	InstrI32TruncF64U
	InstrI32TruncSatF32S
	InstrI32TruncSatF32U
	InstrI32TruncSatF64S
	InstrI32TruncSatF64U
	InstrI64ExtendI32S
	InstrI64ExtendI32U
	InstrI64TruncF32S
	InstrI64TruncF32U
	InstrI64TruncF64S
	InstrI64TruncF64U
	InstrI64TruncSatF32S
	InstrI64TruncSatF32U
	InstrI64TruncSatF64S
	InstrI64TruncSatF64U
	InstrF32ConvertI32S
	InstrF32ConvertI32U
	InstrF32ConvertI64S
	InstrF32ConvertI64U
	InstrF32DemoteF64
	InstrF64ConvertI32S
	InstrF64ConvertI32U
	InstrF64ConvertI64S
	InstrF64ConvertI64U
	InstrF64PromoteF32
	InstrF32Add
	InstrF32Sub
	InstrF32Mul
	InstrF32Div
	InstrF32Sqrt
	InstrF32Neg
	InstrF32Eq
	InstrF32Lt
	InstrF32Gt
	InstrF32Le
	InstrF32Ge
	InstrF32Abs
	InstrF32Ne
	InstrF32Min
	InstrF32Max
	InstrF32Copysign
	InstrF32Ceil
	InstrF32Floor
	InstrF32Trunc
	InstrF32Nearest
	InstrF64Add
	InstrF64Sub
	InstrF64Mul
	InstrF64Div
	InstrF64Sqrt
	InstrF64Abs
	InstrF64Neg
	InstrF64Min
	InstrF64Max
	InstrF64Ceil
	InstrF64Floor
	InstrF64Trunc
	InstrF64Nearest
	InstrF64Eq
	InstrF64Ne
	InstrF64Lt
	InstrF64Gt
	InstrF64Le
	InstrF64Ge
	InstrF64Copysign
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

	// StartFuncIndex is the module function index invoked during instantiation.
	// Nil means the module has no start function section.
	StartFuncIndex *uint32

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

	// Max is the optional maximum table size in elements.
	Max *uint64

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

	// Max is the optional maximum memory size in 64KiB pages.
	Max *uint64

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

	// OffsetExpr is the active-segment offset constant expression, when
	// preserved as instructions. It may contain forms such as global.get that
	// are valid in constant expressions but cannot be pre-evaluated to
	// OffsetI64 during text lowering or binary decoding.
	OffsetExpr []Instruction

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

	// OffsetExpr is the active-segment offset constant expression, when
	// preserved as instructions. It may contain forms such as global.get that
	// are valid in constant expressions but cannot be pre-evaluated to
	// OffsetI64 during text lowering or binary decoding.
	OffsetExpr []Instruction

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
	// For example, tests/wasmspec/scripts/gc/array_init_elem.wast contains:
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

	// LaneIndex is the single-byte lane immediate used by SIMD lane
	// extract/replace and lane-load instructions.
	LaneIndex uint32

	// ShuffleLanes is the 16-byte lane immediate used by i8x16.shuffle.
	ShuffleLanes [16]byte

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

	// BlockType is the optional explicit value-type block result for structured
	// control instructions that are not using BlockTypeUsesIndex.
	BlockType *ValueType

	// BlockTypeUsesIndex reports that structured control block type is encoded
	// as a type index into Module.Types (multi-value block signature).
	BlockTypeUsesIndex bool

	// BlockTypeIndex is the Module.Types index used when BlockTypeUsesIndex is
	// true.
	BlockTypeIndex uint32

	// SelectType is the optional explicit result type immediate used by typed
	// select. Nil means the instruction uses the untyped select form.
	SelectType *ValueType

	// I32Const is the immediate for InstrI32Const.
	I32Const int32

	// I64Const is the immediate for InstrI64Const.
	I64Const int64

	// F32Const is the raw IEEE-754 bits immediate for InstrF32Const.
	F32Const uint32

	// F64Const is the raw IEEE-754 bits immediate for InstrF64Const.
	F64Const uint64

	// V128Const is the raw 16-byte immediate for InstrV128Const.
	V128Const [16]byte

	// SourceLoc is an optional source location string used in diagnostics.
	SourceLoc string
}
