package transport

import (
	"context"
	"fmt"
	"io"

	"google.golang.org/grpc/codes"
)

type Stream struct {
	id  uint32
	st  ServerTransport
	ctx context.Context

	method string
	buf    *recvBuffer
	reader io.Reader
}

type StreamError struct {
	Code codes.Code
	Desc string
}

func (e StreamError) Error() string {
	return fmt.Sprintf("stream error: code=%d desc= %q", e.Code, e.Desc)
}

func (s *Stream) Method() string {
	return s.method
}

func (s *Stream) Context() context.Context {
	return s.ctx
}

func (s *Stream) Read(p []byte) (int, error) {
	return 0, nil
}
