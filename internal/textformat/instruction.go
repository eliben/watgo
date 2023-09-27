package textformat

type Instruction interface {
	isInstr()
}

type PlainInstr struct {
	Name     string
	Operands []Operand
}

type Operand interface {
	isOperand()
}

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
