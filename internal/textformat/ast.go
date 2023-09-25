package textformat

// AST representation of WASM in textual format (including labels, identifiers
// that represent indices, folded instructions etc.)

// TODO: in ASTs we don't deal with index spaces

type Module struct {
	Name  string
	Funcs []*Function
	loc   location
}

type Function struct {
	Id     string
	Export string
	TyUse  TypeUse
	Locals []LocalDecl
	Instrs []Instruction
	loc    location
}

// TypeUse represents the typeuse clause: optional type index and optional
// lists of param/result types.
type TypeUse struct {
	Id      string
	Params  []ParamDecl
	Results []ResultDecl
}

type ParamDecl struct {
	Id string
	Ty Type
}

type ResultDecl struct {
	Ty Type
}

type LocalDecl struct {
	Id string
	Ty Type
}
