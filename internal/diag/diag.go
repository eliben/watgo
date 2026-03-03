package diag

import (
	"fmt"
)

// ErrorList accumulates multiple errors and implements error.
type ErrorList []error

// FromError builds an ErrorList containing err (if non-nil).
func FromError(err error) ErrorList {
	var el ErrorList
	el.Add(err)
	return el
}

// Fromf builds an ErrorList with a single formatted error.
func Fromf(format string, args ...any) ErrorList {
	return FromError(fmt.Errorf(format, args...))
}

func (el *ErrorList) Add(err error) {
	if err == nil {
		return
	}
	*el = append(*el, err)
}

func (el *ErrorList) Addf(format string, args ...any) {
	el.Add(fmt.Errorf(format, args...))
}

func (el ErrorList) HasAny() bool {
	return len(el) > 0
}

// Unwrap returns all contained errors to support multi-error traversal via
// errors.Is / errors.As / errors.AsType.
func (el ErrorList) Unwrap() []error {
	return []error(el)
}

func (el ErrorList) Error() string {
	if len(el) == 0 {
		return "no diagnostics"
	}
	if len(el) == 1 {
		return el[0].Error()
	}
	return fmt.Sprintf("%s (and %d more errors)", el[0], len(el)-1)
}
