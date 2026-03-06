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
