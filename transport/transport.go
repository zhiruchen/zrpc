package transport

import (
	"net"
	"sync"

	"github.com/zhiruchen/zrpc/metadata"
	"google.golang.org/grpc/codes"
)

type ServerTransport interface {

	// ProcessStreams receive and process the stream with the streamProcesser
	ProcessStreams(func(s *Stream))

	// WriteHeader send the header metadata for the given stream
	WriteHeader(s *Stream, md metadata.MD) error
	// Write sends the data for the given stream.
	// Write may not be called on all streams.
	Write(s *Stream, data []byte) error

	// WriteStatus send status to the client
	WriteStatus(s *Stream, code codes.Code, desc string) error

	Close() error
}

// NewServerTransport create underlying server transport
func NewServerTransport(conn net.Conn, maxstreams uint32) (ServerTransport, error) {
	return newHTTP2Server(conn, maxstreams)
}

type recvBuffer struct {
	ch      chan interface{}
	mu      sync.Mutex
	backlog []interface{}
}

func newRecvBuffer() *recvBuffer {
	return &recvBuffer{
		ch: make(chan interface{}, 1),
	}
}

func (rb *recvBuffer) put(i interface{}) {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	if len(rb.backlog) == 0 {
		select {
		case rb.ch <- i:
			return
		default:
		}
	}

	rb.backlog = append(rb.backlog, i)
}
