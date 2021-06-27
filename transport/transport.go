package transport

import (
	"fmt"
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

type ConnectionErr struct {
	Desc string
	temp bool
	err  error
}

func ConnectionErrorf(temp bool, err error, format string, args ...interface{}) ConnectionErr {
	return ConnectionErr{
		temp: temp,
		Desc: fmt.Sprintf(format, args...),
		err:  err,
	}
}

func (e ConnectionErr) Error() string {
	return fmt.Sprintf("connection error: %q", e.Desc)
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
