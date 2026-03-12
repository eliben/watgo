package textformat

// Instruction is one text-format instruction node in the parser AST.
//
// The text AST preserves source syntax shape, so instructions may be either:
//   - PlainInstr: linear token form like "local.get 0" / "i32.add"
//   - FoldedInstr: folded S-expression form like "(i32.add (local.get 0) ...)"
//
// Lowering is the phase that normalizes both forms into canonical wasmir
// instructions.
type Instruction interface {
	isInstr()
	Loc() string
}

// PlainInstr represents one linear instruction in token sequence form.
// Examples:
//
//	local.get 0
//	call $f
//	i64.add
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

// FoldedArg is one argument in a folded instruction form "(op ...)".
// Exactly one of Operand or Instr is expected to be set.
type FoldedArg struct {
	Operand Operand
	Instr   Instruction
}

// Loc returns the source location of this folded argument.
func (fa FoldedArg) Loc() string {
	if fa.Operand != nil {
		return fa.Operand.Loc()
	}
	if fa.Instr != nil {
		return fa.Instr.Loc()
	}
	return ""
}

// FoldedInstr represents one folded instruction in S-expression form.
// Examples:
//
//	(i32.add (i32.const 1) (i32.const 2))
//	(if (result i64) (i64.eqz (local.get 0)) (then ...) (else ...))
//
// This is kept distinct from PlainInstr to preserve source-level syntax
// fidelity in the text AST.
type FoldedInstr struct {
	Name string
	Args []FoldedArg
	loc  location
}

func (*FoldedInstr) isInstr() {}

// Loc returns the source location of this instruction as "line:column".
// It returns an empty string when location is unavailable.
func (fi *FoldedInstr) Loc() string {
	return fi.loc.String()
}

// Operand is one operand in plain or folded instruction forms.
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
