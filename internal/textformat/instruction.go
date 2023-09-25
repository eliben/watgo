package textformat

type Instruction interface {
	isInstr()
}

type PlainInstr struct {
	Kind     string
	Operands []Operand
}

type Operand interface {
	isOperand()
}
