package textformat

import (
	"testing"
)

func TestParseEmptyModule(t *testing.T) {
	{
		// Empty module w/o name
		s := `(module)`
		p := newParser(s)
		m, err := p.parse()

		if err != nil {
			t.Errorf("got %v, want no errors", err)
		}
		if m.Name != "" {
			t.Errorf("got module name %v, want no name", m.Name)
		}
	}

	{
		// Empty module with name
		s := `(module $n42)`
		p := newParser(s)
		m, err := p.parse()

		if err != nil {
			t.Errorf("got %v, want no errors", err)
		}
		if m.Name != "$n42" {
			t.Errorf("got module name %v, want $n42", m.Name)
		}
	}
}
