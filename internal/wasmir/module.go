package wasmir

type ValueType byte

const (
	ValueTypeI32 ValueType = iota
)

type InstrKind uint8

const (
	InstrLocalGet InstrKind = iota
	InstrI32Add
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
}
