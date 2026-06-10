package object

import (
	"bonanza.build/pkg/proto/storage/object"

	"github.com/buildbarn/bb-storage/pkg/util"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Namespace in which an object is stored.
//
// A storage server may partition objects by project or tenant. Each of
// these partitions are called namespaces. It is not possible for
// objects to directly refer to objects in another namespace.
type Namespace struct {
	InstanceName
	ReferenceFormat
}

// NewNamespace creates a Namespace from a Protobuf message that was
// contained in a gRPC request.
func NewNamespace(namespaceMessage *object.Namespace) (Namespace, error) {
	if namespaceMessage == nil {
		return Namespace{}, status.Error(codes.InvalidArgument, "No message provided")
	}
	instanceName, err := NewInstanceName(namespaceMessage.InstanceName)
	if err != nil {
		return Namespace{}, util.StatusWrapf(err, "Invalid instance name %#v", namespaceMessage.InstanceName)
	}
	referenceFormat, err := NewReferenceFormat(namespaceMessage.ReferenceFormat)
	if err != nil {
		return Namespace{}, util.StatusWrap(err, "Invalid reference format")
	}
	return Namespace{
		InstanceName:    instanceName,
		ReferenceFormat: referenceFormat,
	}, nil
}

// NewGlobalReference creates a reference that uniquely refers to an
// object across all namespaces.
func (ns Namespace) NewGlobalReference(rawReference []byte) (GlobalReference, error) {
	localReference, err := ns.ReferenceFormat.NewLocalReference(rawReference)
	if err != nil {
		return GlobalReference{}, err
	}
	return GlobalReference{
		InstanceName:   ns.InstanceName,
		LocalReference: localReference,
	}, nil
}

// ToProto converts a Namespace to a Protobuf message, so that it may be
// sent as part of a gRPC request.
func (ns Namespace) ToProto() *object.Namespace {
	return &object.Namespace{
		InstanceName:    ns.InstanceName.value,
		ReferenceFormat: ns.ReferenceFormat.ToProto(),
	}
}
