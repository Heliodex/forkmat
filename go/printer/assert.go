package printer

import "fmt"

// mustBe returns v as a T. The printer only meets nodes of the kinds their parents hold, so a value of another kind is a bug, and mustBe panics with it.
func mustBe[T any](v any) T {
	t, ok := v.(T)
	if !ok {
		var zero T

		panic(fmt.Sprintf("printer: %T is not %T", v, zero))
	}

	return t
}
