package transport

import (
	"bytes"
	"net"
	"sync"

	"github.com/zhiruchen/zrpc/metadata"
	"golang.org/x/net/http2/hpack"
	"google.golang.org/grpc/codes"
)

// http2 server will implement the transport interface
type http2Server struct {
	conn   net.Conn
	framer *framer

	headerBuf     *bytes.Buffer
	headerEncoder *hpack.Encoder

	mu            sync.Mutex
	activeStreams map[uint32]*Stream
}

func newHTTP2Server(conn net.Conn, maxStreams uint32) (ServerTransport, error) {
	return &http2Server{}, nil
}

func (t *http2Server) ProcessStreams(processor func(s *Stream)) {

}

func (t *http2Server) WriteHeader(s *Stream, md metadata.MD) error {
	return nil
}

func (t *http2Server) Write(s *Stream, data []byte) error {
	return nil
}

func (t *http2Server) WriteStatus(s *Stream, code codes.Code, desc string) error {
	return nil
}

func (t *http2Server) Close() error {
	return nil
}
