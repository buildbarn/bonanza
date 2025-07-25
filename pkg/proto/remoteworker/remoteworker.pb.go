// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.36.6
// 	protoc        v6.31.1
// source: pkg/proto/remoteworker/remoteworker.proto

package remoteworker

import (
	remoteexecution "bonanza.build/pkg/proto/remoteexecution"
	status "google.golang.org/genproto/googleapis/rpc/status"
	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
	anypb "google.golang.org/protobuf/types/known/anypb"
	durationpb "google.golang.org/protobuf/types/known/durationpb"
	emptypb "google.golang.org/protobuf/types/known/emptypb"
	timestamppb "google.golang.org/protobuf/types/known/timestamppb"
	reflect "reflect"
	sync "sync"
	unsafe "unsafe"
)

const (
	// Verify that this generated code is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(20 - protoimpl.MinVersion)
	// Verify that runtime/protoimpl is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(protoimpl.MaxVersion - 20)
)

type CurrentState_Completed_Result int32

const (
	CurrentState_Completed_SUCCEEDED CurrentState_Completed_Result = 0
	CurrentState_Completed_TIMED_OUT CurrentState_Completed_Result = 1
	CurrentState_Completed_FAILED    CurrentState_Completed_Result = 2
)

// Enum value maps for CurrentState_Completed_Result.
var (
	CurrentState_Completed_Result_name = map[int32]string{
		0: "SUCCEEDED",
		1: "TIMED_OUT",
		2: "FAILED",
	}
	CurrentState_Completed_Result_value = map[string]int32{
		"SUCCEEDED": 0,
		"TIMED_OUT": 1,
		"FAILED":    2,
	}
)

func (x CurrentState_Completed_Result) Enum() *CurrentState_Completed_Result {
	p := new(CurrentState_Completed_Result)
	*p = x
	return p
}

func (x CurrentState_Completed_Result) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (CurrentState_Completed_Result) Descriptor() protoreflect.EnumDescriptor {
	return file_pkg_proto_remoteworker_remoteworker_proto_enumTypes[0].Descriptor()
}

func (CurrentState_Completed_Result) Type() protoreflect.EnumType {
	return &file_pkg_proto_remoteworker_remoteworker_proto_enumTypes[0]
}

func (x CurrentState_Completed_Result) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use CurrentState_Completed_Result.Descriptor instead.
func (CurrentState_Completed_Result) EnumDescriptor() ([]byte, []int) {
	return file_pkg_proto_remoteworker_remoteworker_proto_rawDescGZIP(), []int{1, 2, 0}
}

type SynchronizeRequest struct {
	state           protoimpl.MessageState          `protogen:"open.v1"`
	WorkerId        map[string]string               `protobuf:"bytes,1,rep,name=worker_id,json=workerId,proto3" json:"worker_id,omitempty" protobuf_key:"bytes,1,opt,name=key" protobuf_val:"bytes,2,opt,name=value"`
	PublicKeys      []*SynchronizeRequest_PublicKey `protobuf:"bytes,2,rep,name=public_keys,json=publicKeys,proto3" json:"public_keys,omitempty"`
	SizeClass       uint32                          `protobuf:"varint,3,opt,name=size_class,json=sizeClass,proto3" json:"size_class,omitempty"`
	CurrentState    *CurrentState                   `protobuf:"bytes,4,opt,name=current_state,json=currentState,proto3" json:"current_state,omitempty"`
	PreferBeingIdle bool                            `protobuf:"varint,5,opt,name=prefer_being_idle,json=preferBeingIdle,proto3" json:"prefer_being_idle,omitempty"`
	unknownFields   protoimpl.UnknownFields
	sizeCache       protoimpl.SizeCache
}

func (x *SynchronizeRequest) Reset() {
	*x = SynchronizeRequest{}
	mi := &file_pkg_proto_remoteworker_remoteworker_proto_msgTypes[0]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *SynchronizeRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*SynchronizeRequest) ProtoMessage() {}

func (x *SynchronizeRequest) ProtoReflect() protoreflect.Message {
	mi := &file_pkg_proto_remoteworker_remoteworker_proto_msgTypes[0]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use SynchronizeRequest.ProtoReflect.Descriptor instead.
func (*SynchronizeRequest) Descriptor() ([]byte, []int) {
	return file_pkg_proto_remoteworker_remoteworker_proto_rawDescGZIP(), []int{0}
}

func (x *SynchronizeRequest) GetWorkerId() map[string]string {
	if x != nil {
		return x.WorkerId
	}
	return nil
}

func (x *SynchronizeRequest) GetPublicKeys() []*SynchronizeRequest_PublicKey {
	if x != nil {
		return x.PublicKeys
	}
	return nil
}

func (x *SynchronizeRequest) GetSizeClass() uint32 {
	if x != nil {
		return x.SizeClass
	}
	return 0
}

func (x *SynchronizeRequest) GetCurrentState() *CurrentState {
	if x != nil {
		return x.CurrentState
	}
	return nil
}

func (x *SynchronizeRequest) GetPreferBeingIdle() bool {
	if x != nil {
		return x.PreferBeingIdle
	}
	return false
}

type CurrentState struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Types that are valid to be assigned to WorkerState:
	//
	//	*CurrentState_Idle
	//	*CurrentState_Rejected_
	//	*CurrentState_Executing_
	//	*CurrentState_Completed_
	WorkerState   isCurrentState_WorkerState `protobuf_oneof:"worker_state"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *CurrentState) Reset() {
	*x = CurrentState{}
	mi := &file_pkg_proto_remoteworker_remoteworker_proto_msgTypes[1]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *CurrentState) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*CurrentState) ProtoMessage() {}

func (x *CurrentState) ProtoReflect() protoreflect.Message {
	mi := &file_pkg_proto_remoteworker_remoteworker_proto_msgTypes[1]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use CurrentState.ProtoReflect.Descriptor instead.
func (*CurrentState) Descriptor() ([]byte, []int) {
	return file_pkg_proto_remoteworker_remoteworker_proto_rawDescGZIP(), []int{1}
}

func (x *CurrentState) GetWorkerState() isCurrentState_WorkerState {
	if x != nil {
		return x.WorkerState
	}
	return nil
}

func (x *CurrentState) GetIdle() *emptypb.Empty {
	if x != nil {
		if x, ok := x.WorkerState.(*CurrentState_Idle); ok {
			return x.Idle
		}
	}
	return nil
}

func (x *CurrentState) GetRejected() *CurrentState_Rejected {
	if x != nil {
		if x, ok := x.WorkerState.(*CurrentState_Rejected_); ok {
			return x.Rejected
		}
	}
	return nil
}

func (x *CurrentState) GetExecuting() *CurrentState_Executing {
	if x != nil {
		if x, ok := x.WorkerState.(*CurrentState_Executing_); ok {
			return x.Executing
		}
	}
	return nil
}

func (x *CurrentState) GetCompleted() *CurrentState_Completed {
	if x != nil {
		if x, ok := x.WorkerState.(*CurrentState_Completed_); ok {
			return x.Completed
		}
	}
	return nil
}

type isCurrentState_WorkerState interface {
	isCurrentState_WorkerState()
}

type CurrentState_Idle struct {
	Idle *emptypb.Empty `protobuf:"bytes,1,opt,name=idle,proto3,oneof"`
}

type CurrentState_Rejected_ struct {
	Rejected *CurrentState_Rejected `protobuf:"bytes,2,opt,name=rejected,proto3,oneof"`
}

type CurrentState_Executing_ struct {
	Executing *CurrentState_Executing `protobuf:"bytes,3,opt,name=executing,proto3,oneof"`
}

type CurrentState_Completed_ struct {
	Completed *CurrentState_Completed `protobuf:"bytes,4,opt,name=completed,proto3,oneof"`
}

func (*CurrentState_Idle) isCurrentState_WorkerState() {}

func (*CurrentState_Rejected_) isCurrentState_WorkerState() {}

func (*CurrentState_Executing_) isCurrentState_WorkerState() {}

func (*CurrentState_Completed_) isCurrentState_WorkerState() {}

type SynchronizeResponse struct {
	state                 protoimpl.MessageState `protogen:"open.v1"`
	NextSynchronizationAt *timestamppb.Timestamp `protobuf:"bytes,1,opt,name=next_synchronization_at,json=nextSynchronizationAt,proto3" json:"next_synchronization_at,omitempty"`
	DesiredState          *DesiredState          `protobuf:"bytes,2,opt,name=desired_state,json=desiredState,proto3" json:"desired_state,omitempty"`
	unknownFields         protoimpl.UnknownFields
	sizeCache             protoimpl.SizeCache
}

func (x *SynchronizeResponse) Reset() {
	*x = SynchronizeResponse{}
	mi := &file_pkg_proto_remoteworker_remoteworker_proto_msgTypes[2]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *SynchronizeResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*SynchronizeResponse) ProtoMessage() {}

func (x *SynchronizeResponse) ProtoReflect() protoreflect.Message {
	mi := &file_pkg_proto_remoteworker_remoteworker_proto_msgTypes[2]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use SynchronizeResponse.ProtoReflect.Descriptor instead.
func (*SynchronizeResponse) Descriptor() ([]byte, []int) {
	return file_pkg_proto_remoteworker_remoteworker_proto_rawDescGZIP(), []int{2}
}

func (x *SynchronizeResponse) GetNextSynchronizationAt() *timestamppb.Timestamp {
	if x != nil {
		return x.NextSynchronizationAt
	}
	return nil
}

func (x *SynchronizeResponse) GetDesiredState() *DesiredState {
	if x != nil {
		return x.DesiredState
	}
	return nil
}

type DesiredState struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Types that are valid to be assigned to WorkerState:
	//
	//	*DesiredState_VerifyingPublicKeys_
	//	*DesiredState_Idle
	//	*DesiredState_Executing_
	WorkerState   isDesiredState_WorkerState `protobuf_oneof:"worker_state"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *DesiredState) Reset() {
	*x = DesiredState{}
	mi := &file_pkg_proto_remoteworker_remoteworker_proto_msgTypes[3]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *DesiredState) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*DesiredState) ProtoMessage() {}

func (x *DesiredState) ProtoReflect() protoreflect.Message {
	mi := &file_pkg_proto_remoteworker_remoteworker_proto_msgTypes[3]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use DesiredState.ProtoReflect.Descriptor instead.
func (*DesiredState) Descriptor() ([]byte, []int) {
	return file_pkg_proto_remoteworker_remoteworker_proto_rawDescGZIP(), []int{3}
}

func (x *DesiredState) GetWorkerState() isDesiredState_WorkerState {
	if x != nil {
		return x.WorkerState
	}
	return nil
}

func (x *DesiredState) GetVerifyingPublicKeys() *DesiredState_VerifyingPublicKeys {
	if x != nil {
		if x, ok := x.WorkerState.(*DesiredState_VerifyingPublicKeys_); ok {
			return x.VerifyingPublicKeys
		}
	}
	return nil
}

func (x *DesiredState) GetIdle() *emptypb.Empty {
	if x != nil {
		if x, ok := x.WorkerState.(*DesiredState_Idle); ok {
			return x.Idle
		}
	}
	return nil
}

func (x *DesiredState) GetExecuting() *DesiredState_Executing {
	if x != nil {
		if x, ok := x.WorkerState.(*DesiredState_Executing_); ok {
			return x.Executing
		}
	}
	return nil
}

type isDesiredState_WorkerState interface {
	isDesiredState_WorkerState()
}

type DesiredState_VerifyingPublicKeys_ struct {
	VerifyingPublicKeys *DesiredState_VerifyingPublicKeys `protobuf:"bytes,1,opt,name=verifying_public_keys,json=verifyingPublicKeys,proto3,oneof"`
}

type DesiredState_Idle struct {
	Idle *emptypb.Empty `protobuf:"bytes,2,opt,name=idle,proto3,oneof"`
}

type DesiredState_Executing_ struct {
	Executing *DesiredState_Executing `protobuf:"bytes,3,opt,name=executing,proto3,oneof"`
}

func (*DesiredState_VerifyingPublicKeys_) isDesiredState_WorkerState() {}

func (*DesiredState_Idle) isDesiredState_WorkerState() {}

func (*DesiredState_Executing_) isDesiredState_WorkerState() {}

type SynchronizeRequest_PublicKey struct {
	state             protoimpl.MessageState `protogen:"open.v1"`
	PkixPublicKey     []byte                 `protobuf:"bytes,1,opt,name=pkix_public_key,json=pkixPublicKey,proto3" json:"pkix_public_key,omitempty"`
	VerificationZeros []byte                 `protobuf:"bytes,2,opt,name=verification_zeros,json=verificationZeros,proto3" json:"verification_zeros,omitempty"`
	unknownFields     protoimpl.UnknownFields
	sizeCache         protoimpl.SizeCache
}

func (x *SynchronizeRequest_PublicKey) Reset() {
	*x = SynchronizeRequest_PublicKey{}
	mi := &file_pkg_proto_remoteworker_remoteworker_proto_msgTypes[5]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *SynchronizeRequest_PublicKey) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*SynchronizeRequest_PublicKey) ProtoMessage() {}

func (x *SynchronizeRequest_PublicKey) ProtoReflect() protoreflect.Message {
	mi := &file_pkg_proto_remoteworker_remoteworker_proto_msgTypes[5]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use SynchronizeRequest_PublicKey.ProtoReflect.Descriptor instead.
func (*SynchronizeRequest_PublicKey) Descriptor() ([]byte, []int) {
	return file_pkg_proto_remoteworker_remoteworker_proto_rawDescGZIP(), []int{0, 1}
}

func (x *SynchronizeRequest_PublicKey) GetPkixPublicKey() []byte {
	if x != nil {
		return x.PkixPublicKey
	}
	return nil
}

func (x *SynchronizeRequest_PublicKey) GetVerificationZeros() []byte {
	if x != nil {
		return x.VerificationZeros
	}
	return nil
}

type CurrentState_Rejected struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	TaskUuid      string                 `protobuf:"bytes,1,opt,name=task_uuid,json=taskUuid,proto3" json:"task_uuid,omitempty"`
	Reason        *status.Status         `protobuf:"bytes,2,opt,name=reason,proto3" json:"reason,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *CurrentState_Rejected) Reset() {
	*x = CurrentState_Rejected{}
	mi := &file_pkg_proto_remoteworker_remoteworker_proto_msgTypes[6]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *CurrentState_Rejected) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*CurrentState_Rejected) ProtoMessage() {}

func (x *CurrentState_Rejected) ProtoReflect() protoreflect.Message {
	mi := &file_pkg_proto_remoteworker_remoteworker_proto_msgTypes[6]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use CurrentState_Rejected.ProtoReflect.Descriptor instead.
func (*CurrentState_Rejected) Descriptor() ([]byte, []int) {
	return file_pkg_proto_remoteworker_remoteworker_proto_rawDescGZIP(), []int{1, 0}
}

func (x *CurrentState_Rejected) GetTaskUuid() string {
	if x != nil {
		return x.TaskUuid
	}
	return ""
}

func (x *CurrentState_Rejected) GetReason() *status.Status {
	if x != nil {
		return x.Reason
	}
	return nil
}

type CurrentState_Executing struct {
	state         protoimpl.MessageState          `protogen:"open.v1"`
	TaskUuid      string                          `protobuf:"bytes,1,opt,name=task_uuid,json=taskUuid,proto3" json:"task_uuid,omitempty"`
	Event         *remoteexecution.ExecutionEvent `protobuf:"bytes,2,opt,name=event,proto3" json:"event,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *CurrentState_Executing) Reset() {
	*x = CurrentState_Executing{}
	mi := &file_pkg_proto_remoteworker_remoteworker_proto_msgTypes[7]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *CurrentState_Executing) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*CurrentState_Executing) ProtoMessage() {}

func (x *CurrentState_Executing) ProtoReflect() protoreflect.Message {
	mi := &file_pkg_proto_remoteworker_remoteworker_proto_msgTypes[7]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use CurrentState_Executing.ProtoReflect.Descriptor instead.
func (*CurrentState_Executing) Descriptor() ([]byte, []int) {
	return file_pkg_proto_remoteworker_remoteworker_proto_rawDescGZIP(), []int{1, 1}
}

func (x *CurrentState_Executing) GetTaskUuid() string {
	if x != nil {
		return x.TaskUuid
	}
	return ""
}

func (x *CurrentState_Executing) GetEvent() *remoteexecution.ExecutionEvent {
	if x != nil {
		return x.Event
	}
	return nil
}

type CurrentState_Completed struct {
	state                    protoimpl.MessageState          `protogen:"open.v1"`
	TaskUuid                 string                          `protobuf:"bytes,1,opt,name=task_uuid,json=taskUuid,proto3" json:"task_uuid,omitempty"`
	Event                    *remoteexecution.ExecutionEvent `protobuf:"bytes,2,opt,name=event,proto3" json:"event,omitempty"`
	VirtualExecutionDuration *durationpb.Duration            `protobuf:"bytes,3,opt,name=virtual_execution_duration,json=virtualExecutionDuration,proto3" json:"virtual_execution_duration,omitempty"`
	Result                   CurrentState_Completed_Result   `protobuf:"varint,4,opt,name=result,proto3,enum=bonanza.remoteworker.CurrentState_Completed_Result" json:"result,omitempty"`
	unknownFields            protoimpl.UnknownFields
	sizeCache                protoimpl.SizeCache
}

func (x *CurrentState_Completed) Reset() {
	*x = CurrentState_Completed{}
	mi := &file_pkg_proto_remoteworker_remoteworker_proto_msgTypes[8]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *CurrentState_Completed) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*CurrentState_Completed) ProtoMessage() {}

func (x *CurrentState_Completed) ProtoReflect() protoreflect.Message {
	mi := &file_pkg_proto_remoteworker_remoteworker_proto_msgTypes[8]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use CurrentState_Completed.ProtoReflect.Descriptor instead.
func (*CurrentState_Completed) Descriptor() ([]byte, []int) {
	return file_pkg_proto_remoteworker_remoteworker_proto_rawDescGZIP(), []int{1, 2}
}

func (x *CurrentState_Completed) GetTaskUuid() string {
	if x != nil {
		return x.TaskUuid
	}
	return ""
}

func (x *CurrentState_Completed) GetEvent() *remoteexecution.ExecutionEvent {
	if x != nil {
		return x.Event
	}
	return nil
}

func (x *CurrentState_Completed) GetVirtualExecutionDuration() *durationpb.Duration {
	if x != nil {
		return x.VirtualExecutionDuration
	}
	return nil
}

func (x *CurrentState_Completed) GetResult() CurrentState_Completed_Result {
	if x != nil {
		return x.Result
	}
	return CurrentState_Completed_SUCCEEDED
}

type DesiredState_VerifyingPublicKeys struct {
	state                      protoimpl.MessageState `protogen:"open.v1"`
	VerificationPkixPublicKeys [][]byte               `protobuf:"bytes,1,rep,name=verification_pkix_public_keys,json=verificationPkixPublicKeys,proto3" json:"verification_pkix_public_keys,omitempty"`
	unknownFields              protoimpl.UnknownFields
	sizeCache                  protoimpl.SizeCache
}

func (x *DesiredState_VerifyingPublicKeys) Reset() {
	*x = DesiredState_VerifyingPublicKeys{}
	mi := &file_pkg_proto_remoteworker_remoteworker_proto_msgTypes[9]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *DesiredState_VerifyingPublicKeys) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*DesiredState_VerifyingPublicKeys) ProtoMessage() {}

func (x *DesiredState_VerifyingPublicKeys) ProtoReflect() protoreflect.Message {
	mi := &file_pkg_proto_remoteworker_remoteworker_proto_msgTypes[9]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use DesiredState_VerifyingPublicKeys.ProtoReflect.Descriptor instead.
func (*DesiredState_VerifyingPublicKeys) Descriptor() ([]byte, []int) {
	return file_pkg_proto_remoteworker_remoteworker_proto_rawDescGZIP(), []int{3, 0}
}

func (x *DesiredState_VerifyingPublicKeys) GetVerificationPkixPublicKeys() [][]byte {
	if x != nil {
		return x.VerificationPkixPublicKeys
	}
	return nil
}

type DesiredState_Executing struct {
	state                     protoimpl.MessageState  `protogen:"open.v1"`
	TaskUuid                  string                  `protobuf:"bytes,1,opt,name=task_uuid,json=taskUuid,proto3" json:"task_uuid,omitempty"`
	Action                    *remoteexecution.Action `protobuf:"bytes,2,opt,name=action,proto3" json:"action,omitempty"`
	EffectiveExecutionTimeout *durationpb.Duration    `protobuf:"bytes,3,opt,name=effective_execution_timeout,json=effectiveExecutionTimeout,proto3" json:"effective_execution_timeout,omitempty"`
	QueuedTimestamp           *timestamppb.Timestamp  `protobuf:"bytes,4,opt,name=queued_timestamp,json=queuedTimestamp,proto3" json:"queued_timestamp,omitempty"`
	AuxiliaryMetadata         []*anypb.Any            `protobuf:"bytes,5,rep,name=auxiliary_metadata,json=auxiliaryMetadata,proto3" json:"auxiliary_metadata,omitempty"`
	W3CTraceContext           map[string]string       `protobuf:"bytes,6,rep,name=w3c_trace_context,json=w3cTraceContext,proto3" json:"w3c_trace_context,omitempty" protobuf_key:"bytes,1,opt,name=key" protobuf_val:"bytes,2,opt,name=value"`
	unknownFields             protoimpl.UnknownFields
	sizeCache                 protoimpl.SizeCache
}

func (x *DesiredState_Executing) Reset() {
	*x = DesiredState_Executing{}
	mi := &file_pkg_proto_remoteworker_remoteworker_proto_msgTypes[10]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *DesiredState_Executing) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*DesiredState_Executing) ProtoMessage() {}

func (x *DesiredState_Executing) ProtoReflect() protoreflect.Message {
	mi := &file_pkg_proto_remoteworker_remoteworker_proto_msgTypes[10]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use DesiredState_Executing.ProtoReflect.Descriptor instead.
func (*DesiredState_Executing) Descriptor() ([]byte, []int) {
	return file_pkg_proto_remoteworker_remoteworker_proto_rawDescGZIP(), []int{3, 1}
}

func (x *DesiredState_Executing) GetTaskUuid() string {
	if x != nil {
		return x.TaskUuid
	}
	return ""
}

func (x *DesiredState_Executing) GetAction() *remoteexecution.Action {
	if x != nil {
		return x.Action
	}
	return nil
}

func (x *DesiredState_Executing) GetEffectiveExecutionTimeout() *durationpb.Duration {
	if x != nil {
		return x.EffectiveExecutionTimeout
	}
	return nil
}

func (x *DesiredState_Executing) GetQueuedTimestamp() *timestamppb.Timestamp {
	if x != nil {
		return x.QueuedTimestamp
	}
	return nil
}

func (x *DesiredState_Executing) GetAuxiliaryMetadata() []*anypb.Any {
	if x != nil {
		return x.AuxiliaryMetadata
	}
	return nil
}

func (x *DesiredState_Executing) GetW3CTraceContext() map[string]string {
	if x != nil {
		return x.W3CTraceContext
	}
	return nil
}

var File_pkg_proto_remoteworker_remoteworker_proto protoreflect.FileDescriptor

const file_pkg_proto_remoteworker_remoteworker_proto_rawDesc = "" +
	"\n" +
	")pkg/proto/remoteworker/remoteworker.proto\x12\x14bonanza.remoteworker\x1a\x19google/protobuf/any.proto\x1a\x1egoogle/protobuf/duration.proto\x1a\x1bgoogle/protobuf/empty.proto\x1a\x1fgoogle/protobuf/timestamp.proto\x1a\x17google/rpc/status.proto\x1a/pkg/proto/remoteexecution/remoteexecution.proto\"\xf3\x03\n" +
	"\x12SynchronizeRequest\x12S\n" +
	"\tworker_id\x18\x01 \x03(\v26.bonanza.remoteworker.SynchronizeRequest.WorkerIdEntryR\bworkerId\x12S\n" +
	"\vpublic_keys\x18\x02 \x03(\v22.bonanza.remoteworker.SynchronizeRequest.PublicKeyR\n" +
	"publicKeys\x12\x1d\n" +
	"\n" +
	"size_class\x18\x03 \x01(\rR\tsizeClass\x12G\n" +
	"\rcurrent_state\x18\x04 \x01(\v2\".bonanza.remoteworker.CurrentStateR\fcurrentState\x12*\n" +
	"\x11prefer_being_idle\x18\x05 \x01(\bR\x0fpreferBeingIdle\x1a;\n" +
	"\rWorkerIdEntry\x12\x10\n" +
	"\x03key\x18\x01 \x01(\tR\x03key\x12\x14\n" +
	"\x05value\x18\x02 \x01(\tR\x05value:\x028\x01\x1ab\n" +
	"\tPublicKey\x12&\n" +
	"\x0fpkix_public_key\x18\x01 \x01(\fR\rpkixPublicKey\x12-\n" +
	"\x12verification_zeros\x18\x02 \x01(\fR\x11verificationZeros\"\xb5\x06\n" +
	"\fCurrentState\x12,\n" +
	"\x04idle\x18\x01 \x01(\v2\x16.google.protobuf.EmptyH\x00R\x04idle\x12I\n" +
	"\brejected\x18\x02 \x01(\v2+.bonanza.remoteworker.CurrentState.RejectedH\x00R\brejected\x12L\n" +
	"\texecuting\x18\x03 \x01(\v2,.bonanza.remoteworker.CurrentState.ExecutingH\x00R\texecuting\x12L\n" +
	"\tcompleted\x18\x04 \x01(\v2,.bonanza.remoteworker.CurrentState.CompletedH\x00R\tcompleted\x1aS\n" +
	"\bRejected\x12\x1b\n" +
	"\ttask_uuid\x18\x01 \x01(\tR\btaskUuid\x12*\n" +
	"\x06reason\x18\x02 \x01(\v2\x12.google.rpc.StatusR\x06reason\x1ag\n" +
	"\tExecuting\x12\x1b\n" +
	"\ttask_uuid\x18\x01 \x01(\tR\btaskUuid\x12=\n" +
	"\x05event\x18\x02 \x01(\v2'.bonanza.remoteexecution.ExecutionEventR\x05event\x1a\xc1\x02\n" +
	"\tCompleted\x12\x1b\n" +
	"\ttask_uuid\x18\x01 \x01(\tR\btaskUuid\x12=\n" +
	"\x05event\x18\x02 \x01(\v2'.bonanza.remoteexecution.ExecutionEventR\x05event\x12W\n" +
	"\x1avirtual_execution_duration\x18\x03 \x01(\v2\x19.google.protobuf.DurationR\x18virtualExecutionDuration\x12K\n" +
	"\x06result\x18\x04 \x01(\x0e23.bonanza.remoteworker.CurrentState.Completed.ResultR\x06result\"2\n" +
	"\x06Result\x12\r\n" +
	"\tSUCCEEDED\x10\x00\x12\r\n" +
	"\tTIMED_OUT\x10\x01\x12\n" +
	"\n" +
	"\x06FAILED\x10\x02B\x0e\n" +
	"\fworker_state\"\xb2\x01\n" +
	"\x13SynchronizeResponse\x12R\n" +
	"\x17next_synchronization_at\x18\x01 \x01(\v2\x1a.google.protobuf.TimestampR\x15nextSynchronizationAt\x12G\n" +
	"\rdesired_state\x18\x02 \x01(\v2\".bonanza.remoteworker.DesiredStateR\fdesiredState\"\xe0\x06\n" +
	"\fDesiredState\x12l\n" +
	"\x15verifying_public_keys\x18\x01 \x01(\v26.bonanza.remoteworker.DesiredState.VerifyingPublicKeysH\x00R\x13verifyingPublicKeys\x12,\n" +
	"\x04idle\x18\x02 \x01(\v2\x16.google.protobuf.EmptyH\x00R\x04idle\x12L\n" +
	"\texecuting\x18\x03 \x01(\v2,.bonanza.remoteworker.DesiredState.ExecutingH\x00R\texecuting\x1aX\n" +
	"\x13VerifyingPublicKeys\x12A\n" +
	"\x1dverification_pkix_public_keys\x18\x01 \x03(\fR\x1averificationPkixPublicKeys\x1a\xfb\x03\n" +
	"\tExecuting\x12\x1b\n" +
	"\ttask_uuid\x18\x01 \x01(\tR\btaskUuid\x127\n" +
	"\x06action\x18\x02 \x01(\v2\x1f.bonanza.remoteexecution.ActionR\x06action\x12Y\n" +
	"\x1beffective_execution_timeout\x18\x03 \x01(\v2\x19.google.protobuf.DurationR\x19effectiveExecutionTimeout\x12E\n" +
	"\x10queued_timestamp\x18\x04 \x01(\v2\x1a.google.protobuf.TimestampR\x0fqueuedTimestamp\x12C\n" +
	"\x12auxiliary_metadata\x18\x05 \x03(\v2\x14.google.protobuf.AnyR\x11auxiliaryMetadata\x12m\n" +
	"\x11w3c_trace_context\x18\x06 \x03(\v2A.bonanza.remoteworker.DesiredState.Executing.W3cTraceContextEntryR\x0fw3cTraceContext\x1aB\n" +
	"\x14W3cTraceContextEntry\x12\x10\n" +
	"\x03key\x18\x01 \x01(\tR\x03key\x12\x14\n" +
	"\x05value\x18\x02 \x01(\tR\x05value:\x028\x01B\x0e\n" +
	"\fworker_state2t\n" +
	"\x0eOperationQueue\x12b\n" +
	"\vSynchronize\x12(.bonanza.remoteworker.SynchronizeRequest\x1a).bonanza.remoteworker.SynchronizeResponseB&Z$bonanza.build/pkg/proto/remoteworkerb\x06proto3"

var (
	file_pkg_proto_remoteworker_remoteworker_proto_rawDescOnce sync.Once
	file_pkg_proto_remoteworker_remoteworker_proto_rawDescData []byte
)

func file_pkg_proto_remoteworker_remoteworker_proto_rawDescGZIP() []byte {
	file_pkg_proto_remoteworker_remoteworker_proto_rawDescOnce.Do(func() {
		file_pkg_proto_remoteworker_remoteworker_proto_rawDescData = protoimpl.X.CompressGZIP(unsafe.Slice(unsafe.StringData(file_pkg_proto_remoteworker_remoteworker_proto_rawDesc), len(file_pkg_proto_remoteworker_remoteworker_proto_rawDesc)))
	})
	return file_pkg_proto_remoteworker_remoteworker_proto_rawDescData
}

var file_pkg_proto_remoteworker_remoteworker_proto_enumTypes = make([]protoimpl.EnumInfo, 1)
var file_pkg_proto_remoteworker_remoteworker_proto_msgTypes = make([]protoimpl.MessageInfo, 12)
var file_pkg_proto_remoteworker_remoteworker_proto_goTypes = []any{
	(CurrentState_Completed_Result)(0),       // 0: bonanza.remoteworker.CurrentState.Completed.Result
	(*SynchronizeRequest)(nil),               // 1: bonanza.remoteworker.SynchronizeRequest
	(*CurrentState)(nil),                     // 2: bonanza.remoteworker.CurrentState
	(*SynchronizeResponse)(nil),              // 3: bonanza.remoteworker.SynchronizeResponse
	(*DesiredState)(nil),                     // 4: bonanza.remoteworker.DesiredState
	nil,                                      // 5: bonanza.remoteworker.SynchronizeRequest.WorkerIdEntry
	(*SynchronizeRequest_PublicKey)(nil),     // 6: bonanza.remoteworker.SynchronizeRequest.PublicKey
	(*CurrentState_Rejected)(nil),            // 7: bonanza.remoteworker.CurrentState.Rejected
	(*CurrentState_Executing)(nil),           // 8: bonanza.remoteworker.CurrentState.Executing
	(*CurrentState_Completed)(nil),           // 9: bonanza.remoteworker.CurrentState.Completed
	(*DesiredState_VerifyingPublicKeys)(nil), // 10: bonanza.remoteworker.DesiredState.VerifyingPublicKeys
	(*DesiredState_Executing)(nil),           // 11: bonanza.remoteworker.DesiredState.Executing
	nil,                                      // 12: bonanza.remoteworker.DesiredState.Executing.W3cTraceContextEntry
	(*emptypb.Empty)(nil),                    // 13: google.protobuf.Empty
	(*timestamppb.Timestamp)(nil),            // 14: google.protobuf.Timestamp
	(*status.Status)(nil),                    // 15: google.rpc.Status
	(*remoteexecution.ExecutionEvent)(nil),   // 16: bonanza.remoteexecution.ExecutionEvent
	(*durationpb.Duration)(nil),              // 17: google.protobuf.Duration
	(*remoteexecution.Action)(nil),           // 18: bonanza.remoteexecution.Action
	(*anypb.Any)(nil),                        // 19: google.protobuf.Any
}
var file_pkg_proto_remoteworker_remoteworker_proto_depIdxs = []int32{
	5,  // 0: bonanza.remoteworker.SynchronizeRequest.worker_id:type_name -> bonanza.remoteworker.SynchronizeRequest.WorkerIdEntry
	6,  // 1: bonanza.remoteworker.SynchronizeRequest.public_keys:type_name -> bonanza.remoteworker.SynchronizeRequest.PublicKey
	2,  // 2: bonanza.remoteworker.SynchronizeRequest.current_state:type_name -> bonanza.remoteworker.CurrentState
	13, // 3: bonanza.remoteworker.CurrentState.idle:type_name -> google.protobuf.Empty
	7,  // 4: bonanza.remoteworker.CurrentState.rejected:type_name -> bonanza.remoteworker.CurrentState.Rejected
	8,  // 5: bonanza.remoteworker.CurrentState.executing:type_name -> bonanza.remoteworker.CurrentState.Executing
	9,  // 6: bonanza.remoteworker.CurrentState.completed:type_name -> bonanza.remoteworker.CurrentState.Completed
	14, // 7: bonanza.remoteworker.SynchronizeResponse.next_synchronization_at:type_name -> google.protobuf.Timestamp
	4,  // 8: bonanza.remoteworker.SynchronizeResponse.desired_state:type_name -> bonanza.remoteworker.DesiredState
	10, // 9: bonanza.remoteworker.DesiredState.verifying_public_keys:type_name -> bonanza.remoteworker.DesiredState.VerifyingPublicKeys
	13, // 10: bonanza.remoteworker.DesiredState.idle:type_name -> google.protobuf.Empty
	11, // 11: bonanza.remoteworker.DesiredState.executing:type_name -> bonanza.remoteworker.DesiredState.Executing
	15, // 12: bonanza.remoteworker.CurrentState.Rejected.reason:type_name -> google.rpc.Status
	16, // 13: bonanza.remoteworker.CurrentState.Executing.event:type_name -> bonanza.remoteexecution.ExecutionEvent
	16, // 14: bonanza.remoteworker.CurrentState.Completed.event:type_name -> bonanza.remoteexecution.ExecutionEvent
	17, // 15: bonanza.remoteworker.CurrentState.Completed.virtual_execution_duration:type_name -> google.protobuf.Duration
	0,  // 16: bonanza.remoteworker.CurrentState.Completed.result:type_name -> bonanza.remoteworker.CurrentState.Completed.Result
	18, // 17: bonanza.remoteworker.DesiredState.Executing.action:type_name -> bonanza.remoteexecution.Action
	17, // 18: bonanza.remoteworker.DesiredState.Executing.effective_execution_timeout:type_name -> google.protobuf.Duration
	14, // 19: bonanza.remoteworker.DesiredState.Executing.queued_timestamp:type_name -> google.protobuf.Timestamp
	19, // 20: bonanza.remoteworker.DesiredState.Executing.auxiliary_metadata:type_name -> google.protobuf.Any
	12, // 21: bonanza.remoteworker.DesiredState.Executing.w3c_trace_context:type_name -> bonanza.remoteworker.DesiredState.Executing.W3cTraceContextEntry
	1,  // 22: bonanza.remoteworker.OperationQueue.Synchronize:input_type -> bonanza.remoteworker.SynchronizeRequest
	3,  // 23: bonanza.remoteworker.OperationQueue.Synchronize:output_type -> bonanza.remoteworker.SynchronizeResponse
	23, // [23:24] is the sub-list for method output_type
	22, // [22:23] is the sub-list for method input_type
	22, // [22:22] is the sub-list for extension type_name
	22, // [22:22] is the sub-list for extension extendee
	0,  // [0:22] is the sub-list for field type_name
}

func init() { file_pkg_proto_remoteworker_remoteworker_proto_init() }
func file_pkg_proto_remoteworker_remoteworker_proto_init() {
	if File_pkg_proto_remoteworker_remoteworker_proto != nil {
		return
	}
	file_pkg_proto_remoteworker_remoteworker_proto_msgTypes[1].OneofWrappers = []any{
		(*CurrentState_Idle)(nil),
		(*CurrentState_Rejected_)(nil),
		(*CurrentState_Executing_)(nil),
		(*CurrentState_Completed_)(nil),
	}
	file_pkg_proto_remoteworker_remoteworker_proto_msgTypes[3].OneofWrappers = []any{
		(*DesiredState_VerifyingPublicKeys_)(nil),
		(*DesiredState_Idle)(nil),
		(*DesiredState_Executing_)(nil),
	}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: unsafe.Slice(unsafe.StringData(file_pkg_proto_remoteworker_remoteworker_proto_rawDesc), len(file_pkg_proto_remoteworker_remoteworker_proto_rawDesc)),
			NumEnums:      1,
			NumMessages:   12,
			NumExtensions: 0,
			NumServices:   1,
		},
		GoTypes:           file_pkg_proto_remoteworker_remoteworker_proto_goTypes,
		DependencyIndexes: file_pkg_proto_remoteworker_remoteworker_proto_depIdxs,
		EnumInfos:         file_pkg_proto_remoteworker_remoteworker_proto_enumTypes,
		MessageInfos:      file_pkg_proto_remoteworker_remoteworker_proto_msgTypes,
	}.Build()
	File_pkg_proto_remoteworker_remoteworker_proto = out.File
	file_pkg_proto_remoteworker_remoteworker_proto_goTypes = nil
	file_pkg_proto_remoteworker_remoteworker_proto_depIdxs = nil
}
