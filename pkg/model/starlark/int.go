package starlark

import (
	"math/big"
)

// DecodeInt converts a Starlark integer that is stored in a Protobuf
// message as a big-endian two's complement value to a big.Int. An empty
// value corresponds to zero.
func DecodeInt(v []byte) *big.Int {
	i := new(big.Int).SetBytes(v)
	if len(v) > 0 && v[0]&0x80 != 0 {
		i.Sub(i, new(big.Int).Lsh(big.NewInt(1), uint(len(v))*8))
	}
	return i
}

// EncodeInt converts a big.Int to the shortest big-endian two's
// complement value, so that it can be stored as part of a Starlark
// value. It is the inverse of DecodeInt().
func EncodeInt(i *big.Int) []byte {
	if i.Sign() == 0 {
		return nil
	}
	if i.Sign() > 0 {
		return i.FillBytes(make([]byte, i.BitLen()/8+1))
	}
	sizeBytes := new(big.Int).Not(i).BitLen()/8 + 1
	unsigned := new(big.Int).Add(i, new(big.Int).Lsh(big.NewInt(1), uint(sizeBytes)*8))
	return unsigned.FillBytes(make([]byte, sizeBytes))
}
