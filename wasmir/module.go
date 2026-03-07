package wasmir

type ValueType byte

const (
	ValueTypeI32 ValueType = iota
	ValueTypeI64
	ValueTypeF32
)

type InstrKind uint8

const (
	InstrLocalGet InstrKind = iota
	InstrI32Const
	InstrI64Const
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
	TypeIdx uint32
	Locals  []ValueType
	Body    []Instruction
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
}
