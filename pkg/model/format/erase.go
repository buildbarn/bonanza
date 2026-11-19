package format

type erasedUnmarshaler[T any] struct {
	base Unmarshaler[T]
}

// EraseUnmarshaler converts an Unmarshaler yielding values of a
// concrete type to one that yields values of type any. This can be
// used by code that needs to unmarshal values of different types, while
// leaving it up to the caller to determine which type is used.
//
// If the provided Unmarshaler is comparable, the resulting Unmarshaler
// is comparable as well.
func EraseUnmarshaler[T any](base Unmarshaler[T]) Unmarshaler[any] {
	return erasedUnmarshaler[T]{base: base}
}

func (u erasedUnmarshaler[T]) Unmarshal(data []byte) (any, error) {
	value, err := u.base.Unmarshal(data)
	if err != nil {
		return nil, err
	}
	return value, nil
}
