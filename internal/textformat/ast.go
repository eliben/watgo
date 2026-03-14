package textformat

// AST representation of WAT text syntax.
//
// This layer preserves text-level structure and naming (for example folded
// instruction forms and identifier operands like "$x"). It is intentionally
// closer to source than wasmir, which is the normalized semantic IR.
//
// Source locations are kept in unexported `loc` fields and used by parser and
// lowering diagnostics.

// Module is one parsed text-format "(module ...)" declaration.
type Module struct {
	// Id is the optional module identifier (for example "$m").
	// It is empty when the source module is anonymous.
	Id string

	// Types contains parsed type declarations in source order.
	Types []*TypeDecl

	// Tables contains parsed table declarations in source order.
	Tables []*TableDecl

	// Memories contains parsed memory declarations in source order.
	Memories []*MemoryDecl

	// Data contains parsed module-level data segment declarations.
	Data []*DataDecl

	// Globals contains parsed global declarations in source order.
	Globals []*GlobalDecl

	// Elems contains parsed module-level element segment declarations.
	Elems []*ElemDecl

	// Funcs contains parsed function declarations in source order.
	Funcs []*Function

	// loc is the source location of the module form head.
	loc location
}

// TableDecl is one module-level table declaration "(table ...)".
//
// This parser currently supports inline elem syntax like:
//   - (table funcref (elem $f))
type TableDecl struct {
	// Id is the optional table identifier (for example "$t").
	Id string

	// Export is the optional exported name from an inline "(export \"...\")"
	// clause.
	Export string

	// ImportModule is non-empty when this table is imported and stores the
	// import module name.
	ImportModule string

	// ImportName is non-empty when this table is imported and stores the
	// import field name.
	ImportName string

	// Min is the minimum table size in elements.
	Min uint32

	// HasMax reports whether a maximum table size was specified.
	HasMax bool

	// Max is the maximum table size when HasMax is true.
	Max uint32

	// RefTy is the declared table reference element type.
	RefTy Type

	// Init is an optional table-init expression from table sugar forms such as
	// "(table 10 funcref (ref.func $f))".
	Init Instruction

	// ElemRefs are parsed function references from the inline "(elem ...)" list.
	// Each entry is raw source text (identifier or numeric index literal).
	ElemRefs []string

	// ElemExprs are parsed reference-typed constant expressions from inline
	// "(elem ...)" forms such as "(elem (ref.func $f) (ref.null func))".
	ElemExprs []Instruction

	// loc is the source location of the table declaration form head.
	loc location
}

// MemoryDecl is one module-level memory declaration "(memory ...)".
type MemoryDecl struct {
	// Id is the optional memory identifier (for example "$m").
	Id string

	// Export is the optional exported name from an inline "(export \"...\")"
	// clause.
	Export string

	// ImportModule is non-empty when this memory is imported and stores the
	// import module name.
	ImportModule string

	// ImportName is non-empty when this memory is imported and stores the
	// import field name.
	ImportName string

	// Min is the minimum memory size in pages.
	Min uint32

	// HasMax reports whether a maximum memory size was specified.
	HasMax bool

	// Max is the maximum memory size in pages when HasMax is true.
	Max uint32

	// InlineData contains raw string tokens from "(memory (data ...))" sugar.
	InlineData []string

	// loc is the source location of the memory declaration form head.
	loc location
}

// DataDecl is one module-level data segment declaration "(data ...)".
type DataDecl struct {
	// Offset is the active data segment offset expression.
	Offset Instruction

	// Strings contains raw STRING token payloads from source.
	Strings []string

	// loc is the source location of the data declaration form head.
	loc location
}

// GlobalDecl is one module-level global declaration "(global ...)".
type GlobalDecl struct {
	// Id is the optional global identifier (for example "$g").
	Id string

	// Export is the optional exported name from an inline "(export \"...\")"
	// clause.
	Export string

	// ImportModule is non-empty when this global is imported and stores the
	// import module name.
	ImportModule string

	// ImportName is non-empty when this global is imported and stores the import
	// field name.
	ImportName string

	// Mutable reports whether this global declaration uses "(mut ...)".
	Mutable bool

	// Ty is the declared global value type.
	Ty Type

	// Init is the parsed initializer expression.
	Init Instruction

	// loc is the source location of the global declaration form head.
	loc location
}

// Function is one parsed text-format "(func ...)" declaration.
type Function struct {
	// Id is the optional function identifier (for example "$f").
	// It is empty when the function is anonymous.
	Id string

	// Export is the optional exported name from an inline "(export "...")"
	// clause. It is empty when no inline export was declared.
	Export string

	// TyUse is the parsed type-use information for this function, including
	// inline parameter and result declarations.
	TyUse *TypeUse

	// Locals contains parsed non-parameter local declarations in source order.
	Locals []*LocalDecl

	// Instrs contains the function body as text-level instructions.
	Instrs []Instruction

	// loc is the source location of the function form head.
	loc location
}

// TypeUse represents function type-use syntax in text format.
//
// It preserves both the explicit type reference and any inline signature parts
// exactly as parsed from WAT. This includes:
//   - reference-only forms like "(type $sig)",
//   - inline-only forms like "(param i32) (result i32)",
//   - mixed forms like "(type $sig) (param i32) (result i32)".
//
// Example parse shape:
//   - (func (export "type-use-1") (type $sig-1))
//     -> TyUse{Id: "$sig-1", Params: nil, Results: nil}
//   - (func (param i32) (result i32))
//     -> TyUse{Id: "", Params: [{Ty: i32}], Results: [{Ty: i32}]}
type TypeUse struct {
	// Id is the optional referenced type identifier/index from a "(type ...)"
	// use. It may be an identifier (for example "$t") or numeric text
	// (for example "0"). It is empty when no explicit type reference appears.
	Id string

	// Params contains parsed parameter declarations in declaration order.
	Params []*ParamDecl

	// Results contains parsed result declarations in declaration order.
	Results []*ResultDecl

	// loc is the source location of the enclosing type-use form.
	loc location
}

// TypeDecl is one module-level type declaration "(type ...)".
type TypeDecl struct {
	// Id is the optional type identifier (for example "$sig").
	Id string

	// TyUse carries the declared function signature for this type.
	TyUse *TypeUse

	// loc is the source location of the type declaration form head.
	loc location
}

// ParamDecl is one parameter declaration from a "(param ...)" clause.
type ParamDecl struct {
	// Id is the optional parameter identifier (for example "$x").
	// It is empty for anonymous parameters.
	Id string

	// Ty is the declared parameter type.
	Ty Type

	// loc is the source location of the enclosing "(param ...)" clause.
	loc location
}

// ResultDecl is one result declaration from a "(result ...)" clause.
type ResultDecl struct {
	// Ty is the declared result type.
	Ty Type

	// loc is the source location of the enclosing "(result ...)" clause.
	loc location
}

// LocalDecl is one local declaration from a "(local ...)" clause.
type LocalDecl struct {
	// Id is the optional local identifier (for example "$tmp").
	// It is empty for anonymous locals.
	Id string

	// Ty is the declared local type.
	Ty Type

	// loc is the source location of the enclosing "(local ...)" clause.
	loc location
}

// ElemMode classifies an element segment by initialization mode.
//
// This follows the core spec's element segment modes:
// active, passive, and declarative.
type ElemMode uint8

const (
	// ElemModeActive initializes a table at instantiation with an offset.
	ElemModeActive ElemMode = iota

	// ElemModePassive is not applied at instantiation and is used by table.init.
	ElemModePassive

	// ElemModeDeclarative is validated but not available at runtime.
	ElemModeDeclarative
)

// ElemDecl is one module-level element segment declaration "(elem ...)".
type ElemDecl struct {
	// Id is the optional element segment identifier (for example "$e").
	Id string

	// Mode is the element segment mode: active, passive, or declarative.
	Mode ElemMode

	// TableRef is an optional target table identifier/index. Empty means table
	// 0. This is used only for active segments.
	TableRef string

	// Offset is the active segment offset expression.
	Offset Instruction

	// FuncRefs contains function identifiers/indices for function-index
	// payloads.
	FuncRefs []string

	// Exprs contains reference-typed constant-expression payload entries.
	Exprs []Instruction

	// RefTy is the optional explicit payload reference type.
	RefTy Type

	// loc is the source location of the elem declaration form head.
	loc location
}
