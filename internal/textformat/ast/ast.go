package ast

// AST representation of WASM in textual format (including labels, identifiers
// that represent indices, folded instructions etc.)

// TODO: in ASTs we don't deal with index spaces

type Module struct {
	Funcs []Function
}

type Function struct {
	Id      string
	Params  []ParamDecl
	Results []ResultDecl
	Locals  []LocalDecl
	Instrs  []Instruction
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

type Instruction struct {
}
