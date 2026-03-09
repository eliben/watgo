package wasmir

type ValueType byte

const (
	ValueTypeI32 ValueType = iota
	ValueTypeI64
	ValueTypeF32
	ValueTypeF64
)

type InstrKind uint8

const (
	InstrLocalGet InstrKind = iota
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
	InstrI64Add
	InstrI64Sub
	InstrI64Mul
	InstrI64DivS
	InstrI64DivU
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

type ExternalKind uint8

const (
	ExternalKindFunction ExternalKind = iota
)

type Module struct {
	Types   []FuncType
	Funcs   []Function
	Exports []Export
}

type FuncType struct {
	Params  []ValueType
	Results []ValueType
}

type Function struct {
	TypeIdx   uint32
	Locals    []ValueType
	Body      []Instruction
	SourceLoc string
}

type Export struct {
	Name  string
	Kind  ExternalKind
	Index uint32
}

type Instruction struct {
	Kind       InstrKind
	LocalIndex uint32
	I32Const   int32
	I64Const   int64
	F32Const   uint32
	F64Const   uint64
	SourceLoc  string
}
