package format_test

import (
	"strings"
	"testing"

	model_format "bonanza.build/pkg/model/format"
	model_format_pb "bonanza.build/pkg/proto/model/format"

	"github.com/buildbarn/bb-storage/pkg/testutil"
	"github.com/stretchr/testify/require"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestProto(t *testing.T) {
	f := model_format.NewProto[wrapperspb.StringValue]()

	t.Run("ToProto", func(t *testing.T) {
		testutil.RequireEqualProto(t, &model_format_pb.Format{
			Format: &model_format_pb.Format_ProtoTypeName{
				ProtoTypeName: "google.protobuf.StringValue",
			},
		}, f.ToProto())
	})

	t.Run("RoundTrip", func(t *testing.T) {
		data, err := f.NewMarshaler(&wrapperspb.StringValue{Value: "a"}).MarshalBinary()
		require.NoError(t, err)
		require.Equal(t, []byte{0x0a, 0x01, 0x61}, data)

		m, err := f.Unmarshal(data)
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &wrapperspb.StringValue{Value: "a"}, m)
	})

	t.Run("InvalidData", func(t *testing.T) {
		_, err := f.Unmarshal([]byte{0xff})
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	})
}

func TestProtoMarshaler(t *testing.T) {
	t.Run("ArbitraryMessage", func(t *testing.T) {
		// ProtoMarshaler can be instantiated with proto.Message,
		// in which case it should yield the same output as Proto.
		data, err := model_format.ProtoMarshaler[proto.Message]{}.
			NewMarshaler(&wrapperspb.StringValue{Value: "a"}).
			MarshalBinary()
		require.NoError(t, err)
		require.Equal(t, []byte{0x0a, 0x01, 0x61}, data)
	})
}

func TestDynamicProto(t *testing.T) {
	f := model_format.NewDynamicProto((&wrapperspb.StringValue{}).ProtoReflect().Type())

	t.Run("ToProto", func(t *testing.T) {
		testutil.RequireEqualProto(t, &model_format_pb.Format{
			Format: &model_format_pb.Format_ProtoTypeName{
				ProtoTypeName: "google.protobuf.StringValue",
			},
		}, f.ToProto())
	})

	t.Run("RoundTrip", func(t *testing.T) {
		data, err := f.NewMarshaler(&wrapperspb.StringValue{Value: "a"}).MarshalBinary()
		require.NoError(t, err)

		m, err := f.Unmarshal(data)
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &wrapperspb.StringValue{Value: "a"}, m)
	})
}

func TestProtoList(t *testing.T) {
	f := model_format.NewProtoList[wrapperspb.StringValue]()

	t.Run("ToProto", func(t *testing.T) {
		testutil.RequireEqualProto(t, &model_format_pb.Format{
			Format: &model_format_pb.Format_ProtoListTypeName{
				ProtoListTypeName: "google.protobuf.StringValue",
			},
		}, f.ToProto())
	})

	t.Run("Empty", func(t *testing.T) {
		data, err := f.NewMarshaler(nil).MarshalBinary()
		require.NoError(t, err)
		require.Empty(t, data)

		elements, err := f.Unmarshal(nil)
		require.NoError(t, err)
		require.Empty(t, elements)
	})

	t.Run("RoundTrip", func(t *testing.T) {
		// Each element should be prefixed with its size.
		data, err := f.NewMarshaler([]*wrapperspb.StringValue{
			{Value: "a"},
			{Value: "bc"},
		}).MarshalBinary()
		require.NoError(t, err)
		require.Equal(t, []byte{
			0x83, 0x0a, 0x01, 0x61,
			0x84, 0x0a, 0x02, 0x62, 0x63,
		}, data)

		elements, err := f.Unmarshal(data)
		require.NoError(t, err)
		require.Len(t, elements, 2)
		testutil.RequireEqualProto(t, &wrapperspb.StringValue{Value: "a"}, elements[0])
		testutil.RequireEqualProto(t, &wrapperspb.StringValue{Value: "bc"}, elements[1])
	})

	t.Run("SingleAllocation", func(t *testing.T) {
		// Marshaling should allocate a single buffer of the
		// right size, as opposed to growing it for every
		// message. Include a message whose size prefix spans
		// multiple bytes.
		elements := make([]*wrapperspb.StringValue, 0, 100)
		for i := range 100 {
			elements = append(elements, &wrapperspb.StringValue{Value: strings.Repeat("x", 2*i)})
		}
		marshaler := f.NewMarshaler(elements)
		data, err := marshaler.MarshalBinary()
		require.NoError(t, err)
		require.Equal(t, len(data), cap(data))
		require.Equal(t, 1.0, testing.AllocsPerRun(10, func() {
			marshaler.MarshalBinary()
		}))

		unmarshaled, err := f.Unmarshal(data)
		require.NoError(t, err)
		require.Len(t, unmarshaled, 100)
		for i, element := range unmarshaled {
			testutil.RequireEqualProto(t, elements[i], element)
		}
	})

	t.Run("ArbitraryMessages", func(t *testing.T) {
		// ProtoListMarshaler can be instantiated with
		// proto.Message, permitting heterogeneous lists to be
		// marshaled.
		data, err := model_format.ProtoListMarshaler[proto.Message]{}.
			NewMarshaler([]proto.Message{
				&wrapperspb.StringValue{Value: "a"},
				&emptypb.Empty{},
			}).
			MarshalBinary()
		require.NoError(t, err)
		require.Equal(t, []byte{0x83, 0x0a, 0x01, 0x61, 0x80}, data)
	})

	t.Run("InvalidElementLength", func(t *testing.T) {
		// The size prefix announces a trailing byte that is
		// not present.
		_, err := f.Unmarshal([]byte{0x40})
		testutil.RequireEqualStatus(t, status.Error(codes.InvalidArgument, "Invalid element length at offset 0"), err)
	})

	t.Run("ElementTooLarge", func(t *testing.T) {
		_, err := f.Unmarshal([]byte{
			0x83, 0x0a, 0x01, 0x61,
			0x85, 0x0a, 0x01,
		})
		testutil.RequireEqualStatus(t, status.Error(codes.InvalidArgument, "Length of element at offset 4 is 5 bytes, which exceeds maximum permitted size of 2 bytes"), err)
	})

	t.Run("InvalidElement", func(t *testing.T) {
		_, err := f.Unmarshal([]byte{0x81, 0xff})
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	})
}

func TestRaw(t *testing.T) {
	t.Run("ToProto", func(t *testing.T) {
		testutil.RequireEqualProto(t, &model_format_pb.Format{
			Format: &model_format_pb.Format_Raw{
				Raw: &emptypb.Empty{},
			},
		}, model_format.Raw{}.ToProto())
	})

	t.Run("RoundTrip", func(t *testing.T) {
		data, err := model_format.Raw{}.NewMarshaler([]byte("Hello")).MarshalBinary()
		require.NoError(t, err)
		require.Equal(t, []byte("Hello"), data)

		value, err := model_format.Raw{}.Unmarshal(data)
		require.NoError(t, err)
		require.Equal(t, []byte("Hello"), value)
	})
}

func TestToPath(t *testing.T) {
	t.Run("Raw", func(t *testing.T) {
		path, ok := model_format.ToPath(model_format.Raw{}.ToProto())
		require.True(t, ok)
		require.Equal(t, []string{"raw"}, path)
	})

	t.Run("Proto", func(t *testing.T) {
		path, ok := model_format.ToPath(model_format.NewProto[wrapperspb.StringValue]().ToProto())
		require.True(t, ok)
		require.Equal(t, []string{"proto", "google.protobuf.StringValue"}, path)
	})

	t.Run("ProtoList", func(t *testing.T) {
		path, ok := model_format.ToPath(model_format.NewProtoList[wrapperspb.StringValue]().ToProto())
		require.True(t, ok)
		require.Equal(t, []string{"proto_list", "google.protobuf.StringValue"}, path)
	})

	t.Run("Unknown", func(t *testing.T) {
		_, ok := model_format.ToPath(&model_format_pb.Format{})
		require.False(t, ok)
	})
}

func TestEraseUnmarshaler(t *testing.T) {
	u := model_format.EraseUnmarshaler(model_format.NewProto[wrapperspb.StringValue]())

	t.Run("Success", func(t *testing.T) {
		value, err := u.Unmarshal([]byte{0x0a, 0x01, 0x61})
		require.NoError(t, err)
		testutil.RequireEqualProto(t, &wrapperspb.StringValue{Value: "a"}, value.(*wrapperspb.StringValue))
	})

	t.Run("Failure", func(t *testing.T) {
		// Errors should not yield a typed nil pointer wrapped in
		// a non-nil interface value.
		value, err := u.Unmarshal([]byte{0xff})
		require.Equal(t, codes.InvalidArgument, status.Code(err))
		require.Nil(t, value)
	})

	t.Run("Comparable", func(t *testing.T) {
		// Erased unmarshalers should remain usable as keys.
		require.True(t, u == model_format.EraseUnmarshaler(model_format.NewProto[wrapperspb.StringValue]()))
	})
}

func BenchmarkProtoListMarshalBinary(b *testing.B) {
	elements := make([]*wrapperspb.StringValue, 0, 1000)
	for i := range 1000 {
		elements = append(elements, &wrapperspb.StringValue{Value: strings.Repeat("x", i%200)})
	}
	marshaler := model_format.NewProtoList[wrapperspb.StringValue]().NewMarshaler(elements)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := marshaler.MarshalBinary(); err != nil {
			b.Fatal(err)
		}
	}
}
