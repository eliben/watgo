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

	// Funcs contains parsed function declarations in source order.
	Funcs []*Function

	// loc is the source location of the module form head.
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
// It may carry an optional type identifier and inline param/result lists.
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
