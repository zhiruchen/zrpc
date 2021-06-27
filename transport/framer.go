package transport

import (
	"bufio"
	"io"

	"golang.org/x/net/http2"
)

type framer struct {
	reader io.Reader
	writer *bufio.Writer
	fr     *http2.Framer
}

func (f *framer) readFrame() (http2.Frame, error) {
	return f.fr.ReadFrame()
}
