package textformat

import (
	"fmt"
	"testing"
)

func TestParser(t *testing.T) {
	s := `(module $test)`
	p := newParser(s)
	m, err := p.parse()

	fmt.Println(err)
	fmt.Println(m)
}
