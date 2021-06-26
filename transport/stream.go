package transport

import (
	"context"
	"io"
)

type Stream struct {
	id  uint32
	st  ServerTransport
	ctx context.Context

	method string
	buf    *recvBuffer
	reader io.Reader
}
