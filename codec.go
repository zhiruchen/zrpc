package zrpc

import "github.com/golang/protobuf/proto"

// Codec used to encode and decode rpc message
type Codec interface {
	// Marshal encode v
	Marshal(v interface{}) ([]byte, error)

	// Unmarshal decode the data to v
	Unmarshal(data []byte, v interface{}) error

	// String the name of the Codec
	String() string
}

type protoCodec struct{}

func (protoCodec) Marshal(v interface{}) ([]byte, error) {
	return proto.Marshal(v.(proto.Message))
}

func (protoCodec) Unmarhal(data []byte, v interface{}) error {
	return proto.Unmarshal(data, v.(proto.Message))
}

func (protoCodec) String() string {
	return "proto"
}
