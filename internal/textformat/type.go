package textformat

type Type interface {
	isType()
	String() string
}

var basicTypes = map[string]bool{
	"i32":       true,
	"i64":       true,
	"f32":       true,
	"f64":       true,
	"funcref":   true,
	"externref": true,
	"anyref":    true,
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

// RefType is a reference type spelled as "(ref ...)" in text format.
type RefType struct {
	Nullable bool
	HeapType string
}

func (*RefType) isType() {}
func (rt *RefType) String() string {
	if rt.Nullable {
		return "(ref null " + rt.HeapType + ")"
	}
	return "(ref " + rt.HeapType + ")"
}
