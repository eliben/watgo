package diag

import (
	"fmt"
	"strings"
)

// Diagnostic represents a single user-facing issue found during lowering,
// validation or encoding.
type Diagnostic struct {
	Message string
}

func (d Diagnostic) String() string {
	return d.Message
}

// List accumulates diagnostics while allowing the caller to inspect all issues.
type List []Diagnostic

func (dl *List) Add(msg string) {
	*dl = append(*dl, Diagnostic{Message: msg})
}

func (dl *List) Addf(format string, args ...any) {
	dl.Add(fmt.Sprintf(format, args...))
}

func (dl List) HasAny() bool {
	return len(dl) > 0
}

func (dl List) Error() string {
	if len(dl) == 0 {
		return "no diagnostics"
	}
	if len(dl) == 1 {
		return dl[0].Message
	}
	msgs := make([]string, 0, len(dl))
	for _, d := range dl {
		msgs = append(msgs, d.Message)
	}
	return strings.Join(msgs, "; ")
}
