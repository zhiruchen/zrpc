package transport

import (
	"bufio"
	"io"
	"net"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

const (
	defaultFrameSize       = 32 * 1024
	initialHeaderTableSize = uint32(4096)
)

type framer struct {
	reader io.Reader
	writer *bufio.Writer
	fr     *http2.Framer
}

func newFramer(conn net.Conn) *framer {
	f := &framer{
		reader: bufio.NewReaderSize(conn, defaultFrameSize),
		writer: bufio.NewWriterSize(conn, defaultFrameSize),
	}

	f.fr = http2.NewFramer(f.writer, f.reader)
	f.fr.ReadMetaHeaders = hpack.NewDecoder(initialHeaderTableSize, nil)
	return f
}

func (f *framer) readFrame() (http2.Frame, error) {
	return f.fr.ReadFrame()
}

func (f *framer) writeHeaders(flush bool, p http2.HeadersFrameParam) error {
	if err := f.fr.WriteHeaders(p); err != nil {
		return err
	}

	if !flush {
		return nil
	}

	return f.writer.Flush()
}

func (f *framer) writeContinuation(flush bool, streamID uint32, endHeaders bool, p []byte) error {
	if err := f.fr.WriteContinuation(streamID, endHeaders, p); err != nil {
		return err
	}

	if !flush {
		return nil
	}

	return f.writer.Flush()
}

func (f *framer) writeData(flush bool, streamID uint32, endStream bool, data []byte) error {
	if err := f.fr.WriteData(streamID, endStream, data); err != nil {
		return err
	}

	if !flush {
		return nil
	}

	return f.writer.Flush()
}

func (f *framer) writeSettings(flush bool, settings ...http2.Setting) error {
	if err := f.fr.WriteSettings(settings...); err != nil {
		return err
	}

	if flush {
		return f.writer.Flush()
	}

	return nil
}

func (f *framer) writeWindowUpdate(flush bool, streamID uint32, incr uint32) error {
	if err := f.fr.WriteWindowUpdate(streamID, incr); err != nil {
		return err
	}

	if flush {
		return f.writer.Flush()
	}

	return nil
}
