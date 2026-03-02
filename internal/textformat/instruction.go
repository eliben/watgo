package textformat

type Instruction interface {
	isInstr()
}

type PlainInstr struct {
	Name     string
	Operands []Operand
}
func (*PlainInstr) isInstr() {}

type Operand interface {
	isOperand()
}

type IdOperand struct {
	Value string
}

func (*IdOperand) isOperand() {}

type IntOperand struct {
	Value string
}

func (*IntOperand) isOperand() {}

type FloatOperand struct {
	Value string
}

func (*FloatOperand) isOperand() {}

type StringOperand struct {
	Value string
}

func (*StringOperand) isOperand() {}

type KeywordOperand struct {
	Value string
}

func (*KeywordOperand) isOperand() {}

// TODO:
// (control instructions are special)
// For each plain instruction, we need its string representation (to read from
// the input), and some description of its parameters (arity and their types)
type instrName int

const (
	NONE instrName = iota
	I32Load
	I64Load
	F32Load
	F64Load
	// TODO more memory instructions

	I32Const
	I64Const
	F32Const
	F64Const

	I32Add
	I32Sub
)
