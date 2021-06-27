package transport

import (
	"bytes"
	"io"
	"math"
	"net"
	"sync"

	"github.com/zhiruchen/zrpc/log"
	"github.com/zhiruchen/zrpc/metadata"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
	"google.golang.org/grpc/codes"
)

const (
	defaultWindowSize     = 65535
	initialWindowSize     = defaultWindowSize      // for an RPC
	initialConnWindowSize = defaultWindowSize * 16 // for a connection
)

// http2 server will implement the transport interface
type http2Server struct {
	conn   net.Conn
	framer *framer

	headerBuf     *bytes.Buffer
	headerEncoder *hpack.Encoder

	maxStreams uint32
	// controlBuf send the control frames to the control handler
	controlBuf *recvBuffer

	mu            sync.Mutex
	activeStreams map[uint32]*Stream
}

func newHTTP2Server(conn net.Conn, maxStreams uint32) (ServerTransport, error) {
	framer := newFramer(conn)

	settings := []http2.Setting{}
	if maxStreams > 0 {
		settings = append(settings, http2.Setting{ID: http2.SettingMaxConcurrentStreams, Val: maxStreams})
	}

	if maxStreams == 0 {
		maxStreams = math.MaxUint32
	}
	settings = append(settings, http2.Setting{ID: http2.SettingInitialWindowSize, Val: initialWindowSize})

	if err := framer.writeSettings(true, settings...); err != nil {
		return nil, ConnectionErrorf(true, err, "http2Server: %v", err)
	}

	if wp := uint32(initialConnWindowSize - defaultWindowSize); wp > 0 {
		if err := framer.writeWindowUpdate(true, 0, wp); err != nil {
			return nil, ConnectionErrorf(true, err, "http2Server: %v", err)
		}
	}

	var buf bytes.Buffer
	h2Server := &http2Server{
		conn:          conn,
		framer:        framer,
		headerBuf:     &buf,
		headerEncoder: hpack.NewEncoder(&buf),
		maxStreams:    maxStreams,
		controlBuf:    newRecvBuffer(),
		activeStreams: make(map[uint32]*Stream),
	}

	return h2Server, nil
}

// ProcessStreams receive and process the stream
func (t *http2Server) ProcessStreams(processor func(s *Stream)) {
	preface := make([]byte, len(clientPreface))
	if _, err := io.ReadFull(t.conn, preface); err != nil {
		log.Info("[transport.http2Server.HandleStreams] read preface from client: %v", err)
		t.Close()
		return
	}

	if !bytes.Equal(preface, clientPreface) {
		log.Info("[transport.http2Server.HandleStreams] client invalid preface: %q", preface)
		t.Close()
		return
	}

	frame, err := t.framer.readFrame()
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		t.Close()
		return
	}
	if err != nil {
		log.Info("[transport.http2Server.HandleStreams] read frame err: %v", err)
		t.Close()
		return
	}

	sf, ok := frame.(*http2.SettingsFrame)
	if !ok {
		log.Info("[transport.http2Server.HandleStreams] receivede invalid frame type %T from client", frame)
		t.Close()
		return
	}
	t.handleSettings(sf)

	for {
		frame, err := t.framer.readFrame()
		if err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				t.Close()
				return
			}

			log.Info("[transport.http2Server.HandleStreams] read frame err: %v", err)
			t.Close()
			return
		}

		switch fre := frame.(type) {
		case *http2.MetaHeadersFrame:
		case *http2.DataFrame:
		case http2.RSTStreamFrame:
		case *http2.SettingsFrame:
		case *http2.PingFrame:
		case *http2.WindowUpdateFrame:
		case *http2.GoAwayFrame:
		default:
			log.Info("[transport.http2Server.HandleStreams] not handled frame type: %v", fre)
		}
	}
}

func (t *http2Server) handleSettings(f *http2.SettingsFrame) {
	if f.IsAck() {
		return
	}

	var ss []http2.Setting
	f.ForeachSetting(func(s http2.Setting) error {
		ss = append(ss, s)
		return nil
	})

	t.controlBuf.put(&settings{ack: true, ss: ss})
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
