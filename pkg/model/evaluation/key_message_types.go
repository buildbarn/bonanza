package evaluation

import (
	"fmt"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// getSiblingMessageType returns the message type that is declared next
// to the message type of a key. Keys of type Foo.Key are expected to
// have sibling message types like Foo.Value and Foo.Progress, which is
// the naming convention that is enforced by generate_computer.
func getSiblingMessageType(keyMessageName protoreflect.FullName, siblingName protoreflect.Name) (protoreflect.MessageType, error) {
	if keyMessageName.Name() != "Key" {
		return nil, fmt.Errorf("key message type %#v is not named \"Key\"", keyMessageName)
	}
	return protoregistry.GlobalTypes.FindMessageByName(keyMessageName.Parent().Append(siblingName))
}

// GetValueMessageType returns the message type of the value that
// belongs to a key. As values in evaluations are stored without type
// information, it is derived from the type of the key: values of keys
// of type Foo.Key have type Foo.Value.
func GetValueMessageType(keyMessageName protoreflect.FullName) (protoreflect.MessageType, error) {
	return getSiblingMessageType(keyMessageName, "Value")
}

// GetProgressMessageType returns the message type of the progress that
// is reported while evaluating a key. As progress messages are sent
// without type information, it is derived from the type of the key:
// progress of keys of type Foo.Key has type Foo.Progress.
func GetProgressMessageType(keyMessageName protoreflect.FullName) (protoreflect.MessageType, error) {
	return getSiblingMessageType(keyMessageName, "Progress")
}
