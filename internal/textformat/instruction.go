package textformat

type Instruction interface {
	isInstr()
	Loc() string
}

type PlainInstr struct {
	Name     string
	Operands []Operand
	loc      location
}

func (*PlainInstr) isInstr() {}

// Loc returns the source location of this instruction as "line:column".
// It returns an empty string when location is unavailable.
func (pi *PlainInstr) Loc() string {
	return pi.loc.String()
}

type Operand interface {
	isOperand()
	Loc() string
}

type IdOperand struct {
	Value string
	loc   location
}

func (*IdOperand) isOperand() {}

// Loc returns the source location of this operand as "line:column".
// It returns an empty string when location is unavailable.
func (op *IdOperand) Loc() string {
	return op.loc.String()
}

type IntOperand struct {
	Value string
	loc   location
}

func (*IntOperand) isOperand() {}

// Loc returns the source location of this operand as "line:column".
// It returns an empty string when location is unavailable.
func (op *IntOperand) Loc() string {
	return op.loc.String()
}

type FloatOperand struct {
	Value string
	loc   location
}

func (*FloatOperand) isOperand() {}

// Loc returns the source location of this operand as "line:column".
// It returns an empty string when location is unavailable.
func (op *FloatOperand) Loc() string {
	return op.loc.String()
}

type StringOperand struct {
	Value string
	loc   location
}

func (*StringOperand) isOperand() {}

// Loc returns the source location of this operand as "line:column".
// It returns an empty string when location is unavailable.
func (op *StringOperand) Loc() string {
	return op.loc.String()
}

type KeywordOperand struct {
	Value string
	loc   location
}

func (*KeywordOperand) isOperand() {}

// Loc returns the source location of this operand as "line:column".
// It returns an empty string when location is unavailable.
func (op *KeywordOperand) Loc() string {
	return op.loc.String()
}
