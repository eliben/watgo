package textformat

import (
	"github.com/eliben/watgo/internal/instrdef"
	"github.com/eliben/watgo/wasmir"
)

// Instruction is one text-format instruction node in the parser AST.
//
// The text AST preserves source syntax shape, so instructions may be either:
//   - PlainInstr: linear token form like "local.get 0" / "i32.add"
//   - FoldedInstr: folded S-expression form like "(i32.add (local.get 0) ...)"
//   - InstrSeq: a small sequence wrapper for text contexts that syntactically
//     accept more than one instruction expression, such as global initializers
//     in invalid spec tests
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
	// explicitInstrArgs counts folded instruction children that were lowered
	// ahead of this plain instruction.
	explicitInstrArgs int
	// bottomInstrArgs counts explicit folded children that are statically
	// polymorphic bottom.
	bottomInstrArgs int
	loc             location
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

// TryTableInstr preserves the folded try_table form together with its catch
// clauses. It is kept distinct from FoldedInstr because catch clauses are not
// ordinary nested instructions; they are immediate metadata on the structured
// try_table instruction.
type TryTableInstr struct {
	TypeRef     string
	ParamTypes  []Type
	ResultTypes []Type
	Catches     []TryTableCatchClause
	Body        []Instruction
	loc         location
}

func (*TryTableInstr) isInstr() {}

// Loc returns the source location of this instruction as "line:column".
func (ti *TryTableInstr) Loc() string {
	return ti.loc.String()
}

// TryTableCatchClause is one parsed catch clause in the folded try_table form.
// Tag is nil for catch_all and catch_all_ref clauses.
type TryTableCatchClause struct {
	Kind  wasmir.TryTableCatchKind
	Tag   Operand
	Label Operand
	loc   location
}

// InstrSeq preserves a short source-level instruction sequence in places where
// the grammar usually expects a single expression, but the spec tests may
// intentionally provide multiple expressions to exercise invalid-module cases.
type InstrSeq struct {
	Instrs []Instruction
	loc    location
}

func (*InstrSeq) isInstr() {}

// Loc returns the source location of this instruction sequence as
// "line:column". It returns an empty string when location is unavailable.
func (is *InstrSeq) Loc() string {
	return is.loc.String()
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

// TypeOperand is a parsed value/reference type used as a plain structured
// control blocktype operand, for example in `if (result i32)`.
type TypeOperand struct {
	Ty  Type
	loc location
}

func (*TypeOperand) isOperand() {}

// Loc returns the source location of this operand as "line:column".
func (op *TypeOperand) Loc() string {
	return op.loc.String()
}

// StructuredTypeClauseOperand preserves one plain structured control signature
// clause such as `(type $t)`, `(param i32 i64)`, or `(result (ref null $t))`.
type StructuredTypeClauseOperand struct {
	Clause  string
	TypeRef string
	Types   []Type
	loc     location
}

func (*StructuredTypeClauseOperand) isOperand() {}

// Loc returns the source location of this operand as "line:column".
func (op *StructuredTypeClauseOperand) Loc() string {
	return op.loc.String()
}

// instrInfo is the shared instruction metadata stored in instructionInfoByName.
// It gives textformat code the semantic opcode plus the broad syntax family
// needed by parser and lowering decisions.
type instrInfo struct {
	kind        wasmir.InstrKind
	syntaxClass instrdef.InstrSyntaxClass
}

// instructionInfoByName is the single lookup table for instruction facts used
// across textformat. The key is the WAT opcode spelling, for example
// "i32.load" or "table.copy".
var instructionInfoByName map[string]instrInfo

func init() {
	instructionInfoByName = make(map[string]instrInfo)
	for _, def := range instrdef.InstructionDefs() {
		instructionInfoByName[def.TextName] = instrInfo{
			kind:        def.Kind,
			syntaxClass: def.Text.SyntaxClass,
		}
	}
}

// instructionKind looks up the semantic opcode for one text-format opcode.
func instructionKind(name string) (wasmir.InstrKind, bool) {
	entry, ok := instructionInfoByName[name]
	if !ok {
		return 0, false
	}
	return entry.kind, true
}

// instructionHasSyntaxClass reports whether name belongs to the given syntax
// family in the shared instruction catalog.
func instructionHasSyntaxClass(name string, class instrdef.InstrSyntaxClass) bool {
	entry, ok := instructionInfoByName[name]
	return ok && entry.syntaxClass == class
}
