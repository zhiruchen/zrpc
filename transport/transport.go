package transport

import (
	"net"

	"github.com/zhiruchen/zrpc/metadata"
)

type ServerTransport interface {

	// ProcessStreams receive and process the stream with the streamProcesser
	ProcessStreams(func(s *Stream))

	// WriteHeader send the header metadata for the given stream
	WriteHeader(s *Stream, md metadata.MD) error
	// Write sends the data for the given stream.
	// Write may not be called on all streams.
	Write(s *Stream, data []byte) error

	Close() error
}

// NewServerTransport create underlying server transport
func NewServerTransport(conn net.Conn, maxstreams uint32) (ServerTransport, error) {
	return newHTTP2Server(conn, maxstreams)
}

type recvBuffer struct {
}
