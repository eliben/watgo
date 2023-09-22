package textformat

import (
	"testing"
)

func TestSexprSmoke(t *testing.T) {
	s := `(foo bar)`
	lex := newLexer(s)

	sx, err := sexprifyTop(lex)
	if err != nil {
		t.Fatal(err)
	}

	if len(sx.list) != 2 {
		t.Errorf("got len %v, want 2", len(sx.list))
	}
	elem0 := sx.list[0]
	if !(elem0.IsToken() && elem0.tok.value == "foo") {
		t.Errorf("got at 0: %v, want token 'foo'", sx.list[0])
	}
	elem1 := sx.list[1]
	if !(elem1.IsToken() && elem1.tok.value == "bar") {
		t.Errorf("got at 1: %v, want token 'bar'", sx.list[1])
	}
}
