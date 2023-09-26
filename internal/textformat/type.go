package textformat

type Type interface {
	isType()
	String() string
}

var basicTypes = map[string]bool{
	"i32": true,
	"i64": true,
	"f32": true,
	"f64": true,
}

// BasicType is a type that can be described by a single keyword, e.g.
// "i32" or "funcref".
type BasicType struct {
	Name string
}

func (*BasicType) isType() {}
func (bt *BasicType) String() string {
	return bt.Name
}
