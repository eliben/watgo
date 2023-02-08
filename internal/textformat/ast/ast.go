package ast

// AST representation of WASM in textual format (including labels, identifiers
// that represent indices, folded instructions etc.)

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
	Id   string
	Type Type
}

type ResultDecl struct {
	Type Type
}

type LocalDecl struct {
	Id   string
	Type Type
}

type Instruction struct {
}
