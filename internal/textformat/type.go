package textformat

type Type interface {
	isType()
}

var basicTypes = map[string]bool{
	"i32": true,
	"i64": true,
}

// BasicType is a type that can be described by a single keyword, e.g.
// "i32" or "funcref".
type BasicType struct {
	Name string
}

func (*BasicType) isType() {}
