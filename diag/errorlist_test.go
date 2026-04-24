package diag

import (
	"errors"
	"fmt"
	"testing"
)

func TestErrorList_TraversesViaStandardErrorsHelpers(t *testing.T) {
	first := errors.New("first")
	second := errors.New("second")
	err := fmt.Errorf("outer: %w", ErrorList{first, second})

	var errs ErrorList
	if !errors.As(err, &errs) {
		t.Fatalf("errors.As(%T) did not recover ErrorList", err)
	}
	if len(errs) != 2 {
		t.Fatalf("got %d nested errors, want 2", len(errs))
	}
	if errs[0] != first || errs[1] != second {
		t.Fatalf("got nested errors %#v, want [%v %v]", errs, first, second)
	}
	if !errors.Is(err, first) {
		t.Fatal("errors.Is(err, first) = false, want true")
	}
	if !errors.Is(err, second) {
		t.Fatal("errors.Is(err, second) = false, want true")
	}
}
