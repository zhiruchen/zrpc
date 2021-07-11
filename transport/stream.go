package transport

import (
	"context"
	"fmt"
	"io"
	"sync"

	"google.golang.org/grpc/codes"
)

type streamState uint8

const (
	streamActive    streamState = iota
	streamWriteDone             // end stream sent
	streamReadDone              // end stream recevied
	streamDone                  // The stream is finished
)

type Stream struct {
	id     uint32
	st     ServerTransport
	ctx    context.Context
	cancel context.CancelFunc

	method              string
	buf                 *recvBuffer
	reader              io.Reader
	fc                  *inboundFlow
	windowUpdateHandler func(uint32)

	mu         sync.RWMutex
	headerOk   bool // true, if the first header frame is about to send
	state      streamState
	statusCode codes.Code
	statusDesc string
}

type StreamError struct {
	Code codes.Code
	Desc string
}

func StreamErrorf(code codes.Code, desc string) StreamError {
	return StreamError{code, desc}
}

func (e StreamError) Error() string {
	return fmt.Sprintf("stream error: code=%d desc= %q", e.Code, e.Desc)
}

func ContextErr(err error) StreamError {
	switch err {
	case context.DeadlineExceeded:
		return StreamErrorf(codes.DeadlineExceeded, err.Error())
	case context.Canceled:
		return StreamErrorf(codes.Canceled, err.Error())
	}

	panic("Unexpect error from context: " + err.Error())
}

func (s *Stream) Method() string {
	return s.method
}

func (s *Stream) Context() context.Context {
	return s.ctx
}

func (s *Stream) write(m *recvMsg) {
	s.buf.put(m)
}

func (s *Stream) Read(p []byte) (int, error) {
	n, err := s.reader.Read(p)
	if err != nil {
		return n, err
	}

	return 0, nil
}

type streamCtxKey struct{}

func newContextWithStream(ctx context.Context, stream *Stream) context.Context {
	return context.WithValue(ctx, streamCtxKey{}, stream)
}
