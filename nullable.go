package chronos

import "encoding/json"

// Nullable is a field that can be left out, set, or sent as null. Update
// params use it for fields the API can clear. Its zero value leaves the field
// out.
//
//	chronos.ScheduleUpdateParams{
//		Runs:        chronos.NewNullable(10),    // "runs": 10
//		Description: chronos.Null[string](),     // "description": null
//	}                                            // other fields left out
type Nullable[T any] struct {
	value T
	state nullableState
}

type nullableState uint8

const (
	nullableOmitted nullableState = iota
	nullableNull
	nullableSet
)

// NewNullable returns a Nullable that sends v.
func NewNullable[T any](v T) Nullable[T] {
	return Nullable[T]{value: v, state: nullableSet}
}

// Null returns a Nullable that sends JSON null.
func Null[T any]() Nullable[T] {
	return Nullable[T]{state: nullableNull}
}

// IsZero reports whether n leaves its field out. encoding/json calls it for
// fields tagged omitzero.
func (n Nullable[T]) IsZero() bool {
	return n.state == nullableOmitted
}

// MarshalJSON encodes the value, or null.
func (n Nullable[T]) MarshalJSON() ([]byte, error) {
	if n.state != nullableSet {
		return []byte("null"), nil
	}
	return json.Marshal(n.value)
}

func nullableQueryValue(n Nullable[string]) string {
	switch n.state {
	case nullableNull:
		return "null"
	case nullableSet:
		return n.value
	}
	return ""
}
