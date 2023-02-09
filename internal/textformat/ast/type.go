package ast

type Type interface {
	isType()
}

type BasicType struct {
	Name string
}

func (*BasicType) isType() {}
