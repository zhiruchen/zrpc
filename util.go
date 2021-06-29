package zrpc

import (
	"encoding/binary"
	"io"
	"math"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type payloadFormat uint8

const (
	compressNone payloadFormat = iota
	compressMade
)

type parser struct {
	reader io.Reader
	header [5]byte
}

func (p *parser) recvRPCMsg(maxMsgSize uint32) (pf payloadFormat, msg []byte, err error) {
	if _, err := io.ReadFull(p.reader, p.header[:]); err != nil {
		return 0, nil, err
	}

	pf = payloadFormat(p.header[0])
	length := binary.BigEndian.Uint32(p.header[1:])
	if length == 0 {
		return pf, nil, nil
	}

	if length > maxMsgSize {
		return 0, nil, status.Errorf(codes.Internal, "rpc: received msg size: %d, exceeding the max msg size: %d", length, maxMsgSize)
	}

	msg = make([]byte, int(length))
	if _, err := io.ReadFull(p.reader, msg); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}

		return 0, nil, err
	}

	return pf, msg, nil
}

func encode(c Codec, msg interface{}) ([]byte, error) {
	var b []byte
	var length uint

	if msg != nil {
		b, err := c.Marshal(msg)
		if err != nil {
			return nil, err
		}
		length = uint(len(b))
	}

	if length > math.MaxUint32 {
		return nil, status.Errorf(codes.InvalidArgument, "")
	}

	const (
		payloadLen = 1
		sizeLen    = 4
	)

	var buf = make([]byte, payloadLen+sizeLen+len(b))
	buf[0] = byte(compressNone)
	binary.BigEndian.PutUint32(buf[1:], uint32(length))

	copy(buf[5:], b)
	return buf, nil
}
