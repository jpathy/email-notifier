// Code generated by protoc-gen-go-grpc. DO NOT EDIT.

package ctrlrpc

import (
	context "context"
	empty "github.com/golang/protobuf/ptypes/empty"
	wrappers "github.com/golang/protobuf/ptypes/wrappers"
	grpc "google.golang.org/grpc"
	codes "google.golang.org/grpc/codes"
	status "google.golang.org/grpc/status"
)

// This is a compile-time assertion to ensure that this generated file
// is compatible with the grpc package it is being compiled against.
// Requires gRPC-Go v1.32.0 or later.
const _ = grpc.SupportPackageIsVersion7

// ControlEndpointClient is the client API for ControlEndpoint service.
//
// For semantics around ctx use and closing/ending streaming RPCs, please refer to https://pkg.go.dev/google.golang.org/grpc/?tab=doc#ClientConn.NewStream.
type ControlEndpointClient interface {
	SetConfig(ctx context.Context, in *Config, opts ...grpc.CallOption) (*SetConfigResult, error)
	GetConfig(ctx context.Context, in *empty.Empty, opts ...grpc.CallOption) (*Config, error)
	ListSub(ctx context.Context, in *wrappers.StringValue, opts ...grpc.CallOption) (*Subscriptions, error)
	EditSub(ctx context.Context, in *EditSubRequest, opts ...grpc.CallOption) (*Result, error)
	RemoveSub(ctx context.Context, in *wrappers.StringValue, opts ...grpc.CallOption) (*Result, error)
	GetErrors(ctx context.Context, in *wrappers.StringValue, opts ...grpc.CallOption) (ControlEndpoint_GetErrorsClient, error)
	GC(ctx context.Context, in *empty.Empty, opts ...grpc.CallOption) (*Result, error)
}

type controlEndpointClient struct {
	cc grpc.ClientConnInterface
}

func NewControlEndpointClient(cc grpc.ClientConnInterface) ControlEndpointClient {
	return &controlEndpointClient{cc}
}

func (c *controlEndpointClient) SetConfig(ctx context.Context, in *Config, opts ...grpc.CallOption) (*SetConfigResult, error) {
	out := new(SetConfigResult)
	err := c.cc.Invoke(ctx, "/ctrlrpc.ControlEndpoint/SetConfig", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *controlEndpointClient) GetConfig(ctx context.Context, in *empty.Empty, opts ...grpc.CallOption) (*Config, error) {
	out := new(Config)
	err := c.cc.Invoke(ctx, "/ctrlrpc.ControlEndpoint/GetConfig", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *controlEndpointClient) ListSub(ctx context.Context, in *wrappers.StringValue, opts ...grpc.CallOption) (*Subscriptions, error) {
	out := new(Subscriptions)
	err := c.cc.Invoke(ctx, "/ctrlrpc.ControlEndpoint/ListSub", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *controlEndpointClient) EditSub(ctx context.Context, in *EditSubRequest, opts ...grpc.CallOption) (*Result, error) {
	out := new(Result)
	err := c.cc.Invoke(ctx, "/ctrlrpc.ControlEndpoint/EditSub", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *controlEndpointClient) RemoveSub(ctx context.Context, in *wrappers.StringValue, opts ...grpc.CallOption) (*Result, error) {
	out := new(Result)
	err := c.cc.Invoke(ctx, "/ctrlrpc.ControlEndpoint/RemoveSub", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *controlEndpointClient) GetErrors(ctx context.Context, in *wrappers.StringValue, opts ...grpc.CallOption) (ControlEndpoint_GetErrorsClient, error) {
	stream, err := c.cc.NewStream(ctx, &ControlEndpoint_ServiceDesc.Streams[0], "/ctrlrpc.ControlEndpoint/GetErrors", opts...)
	if err != nil {
		return nil, err
	}
	x := &controlEndpointGetErrorsClient{stream}
	if err := x.ClientStream.SendMsg(in); err != nil {
		return nil, err
	}
	if err := x.ClientStream.CloseSend(); err != nil {
		return nil, err
	}
	return x, nil
}

type ControlEndpoint_GetErrorsClient interface {
	Recv() (*DeliveryError, error)
	grpc.ClientStream
}

type controlEndpointGetErrorsClient struct {
	grpc.ClientStream
}

func (x *controlEndpointGetErrorsClient) Recv() (*DeliveryError, error) {
	m := new(DeliveryError)
	if err := x.ClientStream.RecvMsg(m); err != nil {
		return nil, err
	}
	return m, nil
}

func (c *controlEndpointClient) GC(ctx context.Context, in *empty.Empty, opts ...grpc.CallOption) (*Result, error) {
	out := new(Result)
	err := c.cc.Invoke(ctx, "/ctrlrpc.ControlEndpoint/GC", in, out, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ControlEndpointServer is the server API for ControlEndpoint service.
// All implementations must embed UnimplementedControlEndpointServer
// for forward compatibility
type ControlEndpointServer interface {
	SetConfig(context.Context, *Config) (*SetConfigResult, error)
	GetConfig(context.Context, *empty.Empty) (*Config, error)
	ListSub(context.Context, *wrappers.StringValue) (*Subscriptions, error)
	EditSub(context.Context, *EditSubRequest) (*Result, error)
	RemoveSub(context.Context, *wrappers.StringValue) (*Result, error)
	GetErrors(*wrappers.StringValue, ControlEndpoint_GetErrorsServer) error
	GC(context.Context, *empty.Empty) (*Result, error)
	mustEmbedUnimplementedControlEndpointServer()
}

// UnimplementedControlEndpointServer must be embedded to have forward compatible implementations.
type UnimplementedControlEndpointServer struct {
}

func (UnimplementedControlEndpointServer) SetConfig(context.Context, *Config) (*SetConfigResult, error) {
	return nil, status.Errorf(codes.Unimplemented, "method SetConfig not implemented")
}
func (UnimplementedControlEndpointServer) GetConfig(context.Context, *empty.Empty) (*Config, error) {
	return nil, status.Errorf(codes.Unimplemented, "method GetConfig not implemented")
}
func (UnimplementedControlEndpointServer) ListSub(context.Context, *wrappers.StringValue) (*Subscriptions, error) {
	return nil, status.Errorf(codes.Unimplemented, "method ListSub not implemented")
}
func (UnimplementedControlEndpointServer) EditSub(context.Context, *EditSubRequest) (*Result, error) {
	return nil, status.Errorf(codes.Unimplemented, "method EditSub not implemented")
}
func (UnimplementedControlEndpointServer) RemoveSub(context.Context, *wrappers.StringValue) (*Result, error) {
	return nil, status.Errorf(codes.Unimplemented, "method RemoveSub not implemented")
}
func (UnimplementedControlEndpointServer) GetErrors(*wrappers.StringValue, ControlEndpoint_GetErrorsServer) error {
	return status.Errorf(codes.Unimplemented, "method GetErrors not implemented")
}
func (UnimplementedControlEndpointServer) GC(context.Context, *empty.Empty) (*Result, error) {
	return nil, status.Errorf(codes.Unimplemented, "method GC not implemented")
}
func (UnimplementedControlEndpointServer) mustEmbedUnimplementedControlEndpointServer() {}

// UnsafeControlEndpointServer may be embedded to opt out of forward compatibility for this service.
// Use of this interface is not recommended, as added methods to ControlEndpointServer will
// result in compilation errors.
type UnsafeControlEndpointServer interface {
	mustEmbedUnimplementedControlEndpointServer()
}

func RegisterControlEndpointServer(s grpc.ServiceRegistrar, srv ControlEndpointServer) {
	s.RegisterService(&ControlEndpoint_ServiceDesc, srv)
}

func _ControlEndpoint_SetConfig_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(Config)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(ControlEndpointServer).SetConfig(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/ctrlrpc.ControlEndpoint/SetConfig",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(ControlEndpointServer).SetConfig(ctx, req.(*Config))
	}
	return interceptor(ctx, in, info, handler)
}

func _ControlEndpoint_GetConfig_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(empty.Empty)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(ControlEndpointServer).GetConfig(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/ctrlrpc.ControlEndpoint/GetConfig",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(ControlEndpointServer).GetConfig(ctx, req.(*empty.Empty))
	}
	return interceptor(ctx, in, info, handler)
}

func _ControlEndpoint_ListSub_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(wrappers.StringValue)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(ControlEndpointServer).ListSub(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/ctrlrpc.ControlEndpoint/ListSub",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(ControlEndpointServer).ListSub(ctx, req.(*wrappers.StringValue))
	}
	return interceptor(ctx, in, info, handler)
}

func _ControlEndpoint_EditSub_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(EditSubRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(ControlEndpointServer).EditSub(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/ctrlrpc.ControlEndpoint/EditSub",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(ControlEndpointServer).EditSub(ctx, req.(*EditSubRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _ControlEndpoint_RemoveSub_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(wrappers.StringValue)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(ControlEndpointServer).RemoveSub(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/ctrlrpc.ControlEndpoint/RemoveSub",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(ControlEndpointServer).RemoveSub(ctx, req.(*wrappers.StringValue))
	}
	return interceptor(ctx, in, info, handler)
}

func _ControlEndpoint_GetErrors_Handler(srv interface{}, stream grpc.ServerStream) error {
	m := new(wrappers.StringValue)
	if err := stream.RecvMsg(m); err != nil {
		return err
	}
	return srv.(ControlEndpointServer).GetErrors(m, &controlEndpointGetErrorsServer{stream})
}

type ControlEndpoint_GetErrorsServer interface {
	Send(*DeliveryError) error
	grpc.ServerStream
}

type controlEndpointGetErrorsServer struct {
	grpc.ServerStream
}

func (x *controlEndpointGetErrorsServer) Send(m *DeliveryError) error {
	return x.ServerStream.SendMsg(m)
}

func _ControlEndpoint_GC_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(empty.Empty)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(ControlEndpointServer).GC(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/ctrlrpc.ControlEndpoint/GC",
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(ControlEndpointServer).GC(ctx, req.(*empty.Empty))
	}
	return interceptor(ctx, in, info, handler)
}

// ControlEndpoint_ServiceDesc is the grpc.ServiceDesc for ControlEndpoint service.
// It's only intended for direct use with grpc.RegisterService,
// and not to be introspected or modified (even as a copy)
var ControlEndpoint_ServiceDesc = grpc.ServiceDesc{
	ServiceName: "ctrlrpc.ControlEndpoint",
	HandlerType: (*ControlEndpointServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "SetConfig",
			Handler:    _ControlEndpoint_SetConfig_Handler,
		},
		{
			MethodName: "GetConfig",
			Handler:    _ControlEndpoint_GetConfig_Handler,
		},
		{
			MethodName: "ListSub",
			Handler:    _ControlEndpoint_ListSub_Handler,
		},
		{
			MethodName: "EditSub",
			Handler:    _ControlEndpoint_EditSub_Handler,
		},
		{
			MethodName: "RemoveSub",
			Handler:    _ControlEndpoint_RemoveSub_Handler,
		},
		{
			MethodName: "GC",
			Handler:    _ControlEndpoint_GC_Handler,
		},
	},
	Streams: []grpc.StreamDesc{
		{
			StreamName:    "GetErrors",
			Handler:       _ControlEndpoint_GetErrors_Handler,
			ServerStreams: true,
		},
	},
	Metadata: "ctrlrpc/ctrl_message.proto",
}
