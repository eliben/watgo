package textformat

import "fmt"

// errorList represents multiple parse errors reported by the parser on a given
// source. It's loosely modeled on scanner.ErrorList in the Go standard library.
// errorList implements the error interface.
type errorList []error

func (el *errorList) Add(err error) {
	*el = append(*el, err)
}

func (el errorList) Error() string {
	if len(el) == 0 {
		return "no errors"
	} else if len(el) == 1 {
		return el[0].Error()
	} else {
		return fmt.Sprintf("%s (and %d more errors)", el[0], len(el)-1)
	}
}
