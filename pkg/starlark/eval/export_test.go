package eval

// FreezeForTesting makes a value and everything reachable from it
// immutable. It is used to run tests derived from go.starlark.net,
// whose values are frozen after creation.
func FreezeForTesting(v Value) {
	switch x := v.(type) {
	case *memList:
		if !x.immutable {
			x.immutable = true
			for _, element := range x.elements {
				FreezeForTesting(element)
			}
		}
	case memTuple:
		for _, element := range x {
			FreezeForTesting(element)
		}
	case *memDict:
		if !x.immutable {
			x.immutable = true
			for _, entry := range x.entries {
				if entry.key != nil {
					FreezeForTesting(entry.key)
					FreezeForTesting(entry.value)
				}
			}
		}
	case *memSet:
		x.immutable = true
	case *memFunction:
		for _, d := range x.defaults {
			FreezeForTesting(d)
		}
		for _, freeVar := range x.freeVars {
			if freeVar.v != nil {
				FreezeForTesting(freeVar.v)
			}
		}
	case interface{ FreezeForTesting() }:
		x.FreezeForTesting()
	}
}
