package eval

import (
	"fmt"
	"hash/maphash"
	"math"
	"math/big"
	"strconv"
	"strings"

	"bonanza.build/pkg/starlark/syntax"
)

// hashSeed is used to hash strings. Hashes are only used by in-memory
// hash tables, so a random seed per process can be used.
var hashSeed = maphash.MakeSeed()

// String is the type of Starlark strings. Strings are sequences of
// bytes, which are not required to be valid UTF-8.
type String string

// Type returns "string".
func (String) Type() string { return "string" }

// Truth returns whether the string is non-empty.
func (s String) Truth(*Thread) (bool, error) { return len(s) > 0, nil }

// Repr appends the string as a quoted string literal to the printer.
func (s String) Repr(_ *Thread, p *Printer) error {
	p.WriteString(syntax.Quote(string(s)))
	return nil
}

// Hash returns a hash of the string.
func (s String) Hash(*Thread) (uint32, error) {
	return uint32(maphash.String(hashSeed, string(s))), nil
}

// CompareSameType compares strings lexicographically by byte.
func (s String) CompareSameType(_ *Thread, op syntax.Token, y Value, _ int) (bool, error) {
	return threeway(op, strings.Compare(string(s), string(y.(String)))), nil
}

// Len returns the length of the string in bytes.
func (s String) Len(*Thread) (int, error) { return len(s), nil }

// Index returns a string containing the byte at the provided index.
func (s String) Index(_ *Thread, i int) (Value, error) { return s[i : i+1], nil }

// Slice returns a substring.
func (s String) Slice(_ *Thread, start, end, step int) (Value, error) {
	if step == 1 {
		if start < end {
			return s[start:end], nil
		}
		return String(""), nil
	}
	var sb strings.Builder
	for i := start; signum(end-i) == signum(step); i += step {
		sb.WriteByte(s[i])
	}
	return String(sb.String()), nil
}

func signum(x int) int {
	switch {
	case x < 0:
		return -1
	case x > 0:
		return 1
	}
	return 0
}

// Int is the type of Starlark integers, which have arbitrary
// precision. Values that fit in an int64 are stored without
// allocating memory.
type Int struct {
	// Only set if the value does not fit in an int64.
	big   *big.Int
	small int64
}

// MakeInt returns the Starlark integer corresponding to a Go int.
func MakeInt(i int) Int { return Int{small: int64(i)} }

// MakeInt64 returns the Starlark integer corresponding to an int64.
func MakeInt64(i int64) Int { return Int{small: i} }

// MakeBigInt returns the Starlark integer corresponding to a
// *big.Int. The *big.Int must not be modified afterwards.
func MakeBigInt(i *big.Int) Int {
	if i.IsInt64() {
		return Int{small: i.Int64()}
	}
	return Int{big: i}
}

// Int64 returns the value of the integer as an int64, if it fits.
func (i Int) Int64() (int64, bool) {
	if i.big != nil {
		return 0, false
	}
	return i.small, true
}

// Uint64 returns the value of the integer as a uint64, if it fits.
func (i Int) Uint64() (uint64, bool) {
	if i.big != nil {
		if !i.big.IsUint64() {
			return 0, false
		}
		return i.big.Uint64(), true
	}
	if i.small < 0 {
		return 0, false
	}
	return uint64(i.small), true
}

// BigInt returns the value of the integer as a *big.Int. The result
// must not be modified.
func (i Int) BigInt() *big.Int {
	if i.big != nil {
		return i.big
	}
	return big.NewInt(i.small)
}

// Sign returns -1, 0 or 1, depending on the sign of the integer.
func (i Int) Sign() int {
	if i.big != nil {
		return i.big.Sign()
	}
	return signum64(i.small)
}

func signum64(x int64) int {
	switch {
	case x < 0:
		return -1
	case x > 0:
		return 1
	}
	return 0
}

// Type returns "int".
func (Int) Type() string { return "int" }

// Truth returns whether the integer is non-zero.
func (i Int) Truth(*Thread) (bool, error) { return i.Sign() != 0, nil }

// Repr appends the integer in decimal to the printer.
func (i Int) Repr(_ *Thread, p *Printer) error {
	p.WriteString(i.String())
	return nil
}

func (i Int) String() string {
	if i.big != nil {
		return i.big.String()
	}
	return strconv.FormatInt(i.small, 10)
}

// Hash returns a hash of the integer. Floats that have an integral
// value hash to the same value as the equivalent integer.
func (i Int) Hash(*Thread) (uint32, error) {
	if i.big != nil {
		return uint32(maphash.Bytes(hashSeed, i.big.Bytes())) ^ uint32(i.big.Sign()), nil
	}
	return uint32(i.small) ^ uint32(i.small>>32), nil
}

// CompareSameType compares integers numerically.
func (i Int) CompareSameType(_ *Thread, op syntax.Token, y Value, _ int) (bool, error) {
	return threeway(op, i.Cmp(y.(Int))), nil
}

// Cmp returns -1, 0 or 1, depending on whether i is less than, equal
// to, or greater than j.
func (i Int) Cmp(j Int) int {
	if i.big == nil && j.big == nil {
		switch {
		case i.small < j.small:
			return -1
		case i.small > j.small:
			return 1
		}
		return 0
	}
	return i.BigInt().Cmp(j.BigInt())
}

// Add returns i + j.
func (i Int) Add(j Int) Int {
	if i.big == nil && j.big == nil {
		if r := i.small + j.small; (r > i.small) == (j.small > 0) {
			return Int{small: r}
		}
	}
	return MakeBigInt(new(big.Int).Add(i.BigInt(), j.BigInt()))
}

// Sub returns i - j.
func (i Int) Sub(j Int) Int {
	if i.big == nil && j.big == nil {
		if r := i.small - j.small; (r < i.small) == (j.small > 0) {
			return Int{small: r}
		}
	}
	return MakeBigInt(new(big.Int).Sub(i.BigInt(), j.BigInt()))
}

// Mul returns i * j.
func (i Int) Mul(j Int) Int {
	if i.big == nil && j.big == nil {
		if r, ok := mulInt64(i.small, j.small); ok {
			return Int{small: r}
		}
	}
	return MakeBigInt(new(big.Int).Mul(i.BigInt(), j.BigInt()))
}

// mulInt64 returns the product of two int64s, and whether it did not
// overflow.
func mulInt64(a, b int64) (int64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	r := a * b
	if r/b != a || (a == -1 && b == math.MinInt64) || (b == -1 && a == math.MinInt64) {
		return 0, false
	}
	return r, true
}

// FloorDiv returns i // j, rounding towards negative infinity. j must
// be non-zero.
func (i Int) FloorDiv(j Int) Int {
	if i.big == nil && j.big == nil && !(i.small == math.MinInt64 && j.small == -1) {
		q := i.small / j.small
		if (i.small%j.small != 0) && ((i.small < 0) != (j.small < 0)) {
			q--
		}
		return Int{small: q}
	}
	q, m := new(big.Int).DivMod(i.BigInt(), j.BigInt(), new(big.Int))
	if m.Sign() != 0 && j.Sign() < 0 {
		q.Add(q, big.NewInt(1))
	}
	return MakeBigInt(q)
}

// Mod returns i % j, where the result has the same sign as j. j must
// be non-zero.
func (i Int) Mod(j Int) Int {
	if i.big == nil && j.big == nil && j.small != -1 {
		r := i.small % j.small
		if r != 0 && (r < 0) != (j.small < 0) {
			r += j.small
		}
		return Int{small: r}
	}
	_, m := new(big.Int).DivMod(i.BigInt(), j.BigInt(), new(big.Int))
	if m.Sign() != 0 && j.Sign() < 0 {
		m.Add(m, j.BigInt())
	}
	return MakeBigInt(m)
}

// And returns i & j.
func (i Int) And(j Int) Int {
	if i.big == nil && j.big == nil {
		return Int{small: i.small & j.small}
	}
	return MakeBigInt(new(big.Int).And(i.BigInt(), j.BigInt()))
}

// Or returns i | j.
func (i Int) Or(j Int) Int {
	if i.big == nil && j.big == nil {
		return Int{small: i.small | j.small}
	}
	return MakeBigInt(new(big.Int).Or(i.BigInt(), j.BigInt()))
}

// Xor returns i ^ j.
func (i Int) Xor(j Int) Int {
	if i.big == nil && j.big == nil {
		return Int{small: i.small ^ j.small}
	}
	return MakeBigInt(new(big.Int).Xor(i.BigInt(), j.BigInt()))
}

// Not returns ~i.
func (i Int) Not() Int {
	if i.big == nil {
		return Int{small: ^i.small}
	}
	return MakeBigInt(new(big.Int).Not(i.big))
}

// Neg returns -i.
func (i Int) Neg() Int {
	if i.big == nil && i.small != math.MinInt64 {
		return Int{small: -i.small}
	}
	return MakeBigInt(new(big.Int).Neg(i.BigInt()))
}

// Lsh returns i << n.
func (i Int) Lsh(n uint) Int {
	return MakeBigInt(new(big.Int).Lsh(i.BigInt(), n))
}

// Rsh returns i >> n.
func (i Int) Rsh(n uint) Int {
	if i.big == nil {
		if n >= 64 {
			return Int{small: i.small >> 63}
		}
		return Int{small: i.small >> n}
	}
	return MakeBigInt(new(big.Int).Rsh(i.big, n))
}

// Float converts the integer to a float. It fails if the integer is
// too large to be represented.
func (i Int) Float() (Float, error) {
	if i.big == nil {
		return Float(i.small), nil
	}
	f, _ := new(big.Float).SetInt(i.big).Float64()
	if math.IsInf(f, 0) {
		return 0, fmt.Errorf("int too large to convert to float")
	}
	return Float(f), nil
}

// Float is the type of Starlark floating point numbers.
type Float float64

// Type returns "float".
func (Float) Type() string { return "float" }

// Truth returns whether the float is non-zero.
func (f Float) Truth(*Thread) (bool, error) { return f != 0, nil }

// Repr appends the float to the printer. Floats with an integral value
// are printed with a trailing ".0", so that they can be distinguished
// from integers.
func (f Float) Repr(_ *Thread, p *Printer) error {
	p.WriteString(f.String())
	return nil
}

func (f Float) String() string {
	switch {
	case math.IsInf(float64(f), 1):
		return "+inf"
	case math.IsInf(float64(f), -1):
		return "-inf"
	case math.IsNaN(float64(f)):
		return "nan"
	}
	s := strconv.FormatFloat(float64(f), 'g', -1, 64)
	if !strings.ContainsAny(s, ".e") {
		s += ".0"
	}
	return s
}

// Hash returns a hash of the float. Floats with an integral value
// hash to the same value as the equivalent integer.
func (f Float) Hash(t *Thread) (uint32, error) {
	if i, ok := f.integral(); ok {
		return i.Hash(t)
	}
	return uint32(math.Float64bits(float64(f))>>32) ^ uint32(math.Float64bits(float64(f))), nil
}

// integral returns the float as an integer, if it has an integral
// value.
func (f Float) integral() (Int, bool) {
	if math.IsInf(float64(f), 0) || math.IsNaN(float64(f)) || f != Float(math.Trunc(float64(f))) {
		return Int{}, false
	}
	if f >= math.MinInt64 && f < math.MaxInt64 {
		return MakeInt64(int64(f)), true
	}
	i, _ := big.NewFloat(float64(f)).Int(nil)
	return MakeBigInt(i), true
}

// CompareSameType compares floats numerically. NaN compares greater
// than all other values, and equal to itself, so that floats are
// totally ordered.
func (f Float) CompareSameType(_ *Thread, op syntax.Token, y Value, _ int) (bool, error) {
	return threeway(op, compareFloats(float64(f), float64(y.(Float)))), nil
}

func compareFloats(x, y float64) int {
	switch {
	case x < y:
		return -1
	case x > y:
		return 1
	case x == y:
		return 0
	case math.IsNaN(x):
		if math.IsNaN(y) {
			return 0
		}
		return 1
	default:
		return -1
	}
}

// compareIntFloat compares an integer against a float.
func compareIntFloat(i Int, f Float) int {
	if math.IsNaN(float64(f)) {
		return -1
	}
	if math.IsInf(float64(f), 0) {
		if f > 0 {
			return -1
		}
		return 1
	}
	bf := new(big.Float).SetInt(i.BigInt())
	return bf.Cmp(big.NewFloat(float64(f)))
}

// floorDivFloats returns x // y for floats.
func floorDivFloats(x, y float64) float64 {
	return math.Floor(x / y)
}

// modFloats returns x % y for floats, where the result has the same
// sign as y.
func modFloats(x, y float64) float64 {
	r := math.Mod(x, y)
	if r != 0 && (r < 0) != (y < 0) {
		r += y
	}
	return r
}
