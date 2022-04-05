// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.26.0
// 	protoc        v3.16.0
// source: pkg/peerhealth/peerhealth.proto

package peerhealth

import (
	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
	reflect "reflect"
	sync "sync"
)

const (
	// Verify that this generated code is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(20 - protoimpl.MinVersion)
	// Verify that runtime/protoimpl is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(protoimpl.MaxVersion - 20)
)

type HealthRequest struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	NodeName string `protobuf:"bytes,1,opt,name=nodeName,proto3" json:"nodeName,omitempty"`
}

func (x *HealthRequest) Reset() {
	*x = HealthRequest{}
	if protoimpl.UnsafeEnabled {
		mi := &file_pkg_peerhealth_peerhealth_proto_msgTypes[0]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *HealthRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*HealthRequest) ProtoMessage() {}

func (x *HealthRequest) ProtoReflect() protoreflect.Message {
	mi := &file_pkg_peerhealth_peerhealth_proto_msgTypes[0]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use HealthRequest.ProtoReflect.Descriptor instead.
func (*HealthRequest) Descriptor() ([]byte, []int) {
	return file_pkg_peerhealth_peerhealth_proto_rawDescGZIP(), []int{0}
}

func (x *HealthRequest) GetNodeName() string {
	if x != nil {
		return x.NodeName
	}
	return ""
}

type HealthResponse struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	Status int32 `protobuf:"varint,1,opt,name=status,proto3" json:"status,omitempty"`
}

func (x *HealthResponse) Reset() {
	*x = HealthResponse{}
	if protoimpl.UnsafeEnabled {
		mi := &file_pkg_peerhealth_peerhealth_proto_msgTypes[1]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *HealthResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*HealthResponse) ProtoMessage() {}

func (x *HealthResponse) ProtoReflect() protoreflect.Message {
	mi := &file_pkg_peerhealth_peerhealth_proto_msgTypes[1]
	if protoimpl.UnsafeEnabled && x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use HealthResponse.ProtoReflect.Descriptor instead.
func (*HealthResponse) Descriptor() ([]byte, []int) {
	return file_pkg_peerhealth_peerhealth_proto_rawDescGZIP(), []int{1}
}

func (x *HealthResponse) GetStatus() int32 {
	if x != nil {
		return x.Status
	}
	return 0
}

var File_pkg_peerhealth_peerhealth_proto protoreflect.FileDescriptor

var file_pkg_peerhealth_peerhealth_proto_rawDesc = []byte{
	0x0a, 0x1f, 0x70, 0x6b, 0x67, 0x2f, 0x70, 0x65, 0x65, 0x72, 0x68, 0x65, 0x61, 0x6c, 0x74, 0x68,
	0x2f, 0x70, 0x65, 0x65, 0x72, 0x68, 0x65, 0x61, 0x6c, 0x74, 0x68, 0x2e, 0x70, 0x72, 0x6f, 0x74,
	0x6f, 0x12, 0x1a, 0x73, 0x65, 0x6c, 0x66, 0x6e, 0x6f, 0x64, 0x65, 0x72, 0x65, 0x6d, 0x65, 0x64,
	0x69, 0x61, 0x74, 0x69, 0x6f, 0x6e, 0x2e, 0x68, 0x65, 0x61, 0x6c, 0x74, 0x68, 0x22, 0x2b, 0x0a,
	0x0d, 0x48, 0x65, 0x61, 0x6c, 0x74, 0x68, 0x52, 0x65, 0x71, 0x75, 0x65, 0x73, 0x74, 0x12, 0x1a,
	0x0a, 0x08, 0x6e, 0x6f, 0x64, 0x65, 0x4e, 0x61, 0x6d, 0x65, 0x18, 0x01, 0x20, 0x01, 0x28, 0x09,
	0x52, 0x08, 0x6e, 0x6f, 0x64, 0x65, 0x4e, 0x61, 0x6d, 0x65, 0x22, 0x28, 0x0a, 0x0e, 0x48, 0x65,
	0x61, 0x6c, 0x74, 0x68, 0x52, 0x65, 0x73, 0x70, 0x6f, 0x6e, 0x73, 0x65, 0x12, 0x16, 0x0a, 0x06,
	0x73, 0x74, 0x61, 0x74, 0x75, 0x73, 0x18, 0x01, 0x20, 0x01, 0x28, 0x05, 0x52, 0x06, 0x73, 0x74,
	0x61, 0x74, 0x75, 0x73, 0x32, 0x72, 0x0a, 0x0a, 0x50, 0x65, 0x65, 0x72, 0x48, 0x65, 0x61, 0x6c,
	0x74, 0x68, 0x12, 0x64, 0x0a, 0x09, 0x49, 0x73, 0x48, 0x65, 0x61, 0x6c, 0x74, 0x68, 0x79, 0x12,
	0x29, 0x2e, 0x73, 0x65, 0x6c, 0x66, 0x6e, 0x6f, 0x64, 0x65, 0x72, 0x65, 0x6d, 0x65, 0x64, 0x69,
	0x61, 0x74, 0x69, 0x6f, 0x6e, 0x2e, 0x68, 0x65, 0x61, 0x6c, 0x74, 0x68, 0x2e, 0x48, 0x65, 0x61,
	0x6c, 0x74, 0x68, 0x52, 0x65, 0x71, 0x75, 0x65, 0x73, 0x74, 0x1a, 0x2a, 0x2e, 0x73, 0x65, 0x6c,
	0x66, 0x6e, 0x6f, 0x64, 0x65, 0x72, 0x65, 0x6d, 0x65, 0x64, 0x69, 0x61, 0x74, 0x69, 0x6f, 0x6e,
	0x2e, 0x68, 0x65, 0x61, 0x6c, 0x74, 0x68, 0x2e, 0x48, 0x65, 0x61, 0x6c, 0x74, 0x68, 0x52, 0x65,
	0x73, 0x70, 0x6f, 0x6e, 0x73, 0x65, 0x22, 0x00, 0x42, 0x10, 0x5a, 0x0e, 0x70, 0x6b, 0x67, 0x2f,
	0x70, 0x65, 0x65, 0x72, 0x68, 0x65, 0x61, 0x6c, 0x74, 0x68, 0x62, 0x06, 0x70, 0x72, 0x6f, 0x74,
	0x6f, 0x33,
}

var (
	file_pkg_peerhealth_peerhealth_proto_rawDescOnce sync.Once
	file_pkg_peerhealth_peerhealth_proto_rawDescData = file_pkg_peerhealth_peerhealth_proto_rawDesc
)

func file_pkg_peerhealth_peerhealth_proto_rawDescGZIP() []byte {
	file_pkg_peerhealth_peerhealth_proto_rawDescOnce.Do(func() {
		file_pkg_peerhealth_peerhealth_proto_rawDescData = protoimpl.X.CompressGZIP(file_pkg_peerhealth_peerhealth_proto_rawDescData)
	})
	return file_pkg_peerhealth_peerhealth_proto_rawDescData
}

var file_pkg_peerhealth_peerhealth_proto_msgTypes = make([]protoimpl.MessageInfo, 2)
var file_pkg_peerhealth_peerhealth_proto_goTypes = []interface{}{
	(*HealthRequest)(nil),  // 0: selfnoderemediation.health.HealthRequest
	(*HealthResponse)(nil), // 1: selfnoderemediation.health.HealthResponse
}
var file_pkg_peerhealth_peerhealth_proto_depIdxs = []int32{
	0, // 0: selfnoderemediation.health.PeerHealth.IsHealthy:input_type -> selfnoderemediation.health.HealthRequest
	1, // 1: selfnoderemediation.health.PeerHealth.IsHealthy:output_type -> selfnoderemediation.health.HealthResponse
	1, // [1:2] is the sub-list for method output_type
	0, // [0:1] is the sub-list for method input_type
	0, // [0:0] is the sub-list for extension type_name
	0, // [0:0] is the sub-list for extension extendee
	0, // [0:0] is the sub-list for field type_name
}

func init() { file_pkg_peerhealth_peerhealth_proto_init() }
func file_pkg_peerhealth_peerhealth_proto_init() {
	if File_pkg_peerhealth_peerhealth_proto != nil {
		return
	}
	if !protoimpl.UnsafeEnabled {
		file_pkg_peerhealth_peerhealth_proto_msgTypes[0].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*HealthRequest); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_pkg_peerhealth_peerhealth_proto_msgTypes[1].Exporter = func(v interface{}, i int) interface{} {
			switch v := v.(*HealthResponse); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
	}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: file_pkg_peerhealth_peerhealth_proto_rawDesc,
			NumEnums:      0,
			NumMessages:   2,
			NumExtensions: 0,
			NumServices:   1,
		},
		GoTypes:           file_pkg_peerhealth_peerhealth_proto_goTypes,
		DependencyIndexes: file_pkg_peerhealth_peerhealth_proto_depIdxs,
		MessageInfos:      file_pkg_peerhealth_peerhealth_proto_msgTypes,
	}.Build()
	File_pkg_peerhealth_peerhealth_proto = out.File
	file_pkg_peerhealth_peerhealth_proto_rawDesc = nil
	file_pkg_peerhealth_peerhealth_proto_goTypes = nil
	file_pkg_peerhealth_peerhealth_proto_depIdxs = nil
}