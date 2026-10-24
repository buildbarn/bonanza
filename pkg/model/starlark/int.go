package starlark

import (
	"math/big"

	model_starlark_pb "bonanza.build/pkg/proto/model/starlark"
)

// DecodeInt converts a Starlark integer that is stored in a Protobuf
// message to a big.Int.
func DecodeInt(v *model_starlark_pb.Int) *big.Int {
	i := new(big.Int).SetBytes(v.GetAbsoluteValue())
	if v.GetNegative() {
		i.Neg(i)
	}
	return i
}

// EncodeInt converts a big.Int to a Protobuf message, so that it can be
// stored as part of a Starlark value. It is the inverse of DecodeInt().
func EncodeInt(i *big.Int) *model_starlark_pb.Int {
	return &model_starlark_pb.Int{
		AbsoluteValue: i.Bytes(),
		Negative:      i.Sign() < 0,
	}
}
