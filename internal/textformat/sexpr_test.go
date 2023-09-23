package textformat

import (
	"strings"
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
		t.Errorf("got at 0: %v, want token 'foo'", elem0)
	}
	elem1 := sx.list[1]
	if !(elem1.IsToken() && elem1.tok.value == "bar") {
		t.Errorf("got at 1: %v, want token 'bar'", elem1)
	}
}

func TestEmptyList(t *testing.T) {
	s := `(foo () bar)`
	lex := newLexer(s)

	sx, err := sexprifyTop(lex)
	if err != nil {
		t.Fatal(err)
	}

	elem1 := sx.list[1]
	if !(elem1.IsToken() && !elem1.IsList() && elem1.tok.name == EMPTY) {
		t.Errorf("got at 1: %v, want EMPTY", elem1)
	}
}

func showForTest(sx *sexpr) string {
	if len(sx.list) > 0 {
		var parts []string
		for _, sub := range sx.list {
			parts = append(parts, showForTest(sub))
		}
		return "(" + strings.Join(parts, " ") + ")"
	} else {
		return tokenNames[sx.tok.name]
	}
}

func TestSexprLists(t *testing.T) {
	var tests = []struct {
		input string
		want  string
	}{
		{`(  foo )`, "(KEYWORD)"},
		{`(  foo ($id "str")  )`, "(KEYWORD (ID STRING))"},
		{`(25 (1.5 "str") foo ($id "str"))`, "(INT (FLOAT STRING) KEYWORD (ID STRING))"},
		{`(((foo)))`, "(((KEYWORD)))"},
		{`(x () (()) y)`, "(KEYWORD EMPTY (EMPTY) KEYWORD)"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			lex := newLexer(tt.input)
			sx, err := sexprifyTop(lex)
			if err != nil {
				t.Fatal(err)
			}

			got := showForTest(sx)
			if got != tt.want {
				t.Errorf("got %s, want %s", got, tt.want)
			}
		})
	}
}

func TestErrorUnterminatedLparen(t *testing.T) {
	var tests = []struct {
		input string
		where string
	}{
		{`(foo`, "1:1"},
		{`     ( (abo) (bobo) (foo ()`, "1:21"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			lex := newLexer(tt.input)
			_, err := sexprifyTop(lex)
			if err == nil {
				t.Fatal("got no error, want error")
			}

			if !strings.Contains(err.Error(), tt.where) {
				t.Errorf("got error %v, want to find %s", err, tt.where)
			}
		})
	}
}
