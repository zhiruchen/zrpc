package transport

import (
	"bytes"
	"context"
	"fmt"
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

type serverState uint

const (
	reachable serverState = iota
	unreachable
	closing
	stoping
)

// http2 server will implement the transport interface
type http2Server struct {
	conn        net.Conn
	maxStreamID uint32

	// shutdown is closed when http2Server is closed
	shutdownCh chan struct{}
	framer     *framer

	headerBuf     *bytes.Buffer
	headerEncoder *hpack.Encoder

	maxStreams uint32
	// controlBuf send the control frames to the control handler
	controlBuf *recvBuffer
	fc         *inboundFlow

	mu            sync.Mutex
	state         serverState
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

	buf := &bytes.Buffer{}
	h2Server := &http2Server{
		conn:          conn,
		framer:        framer,
		headerBuf:     buf,
		headerEncoder: hpack.NewEncoder(buf),
		maxStreams:    maxStreams,
		controlBuf:    newRecvBuffer(),
		fc:            &inboundFlow{limit: initialConnWindowSize},
		activeStreams: make(map[uint32]*Stream),
	}

	return h2Server, nil
}

// ProcessStreams receive and process the stream
func (t *http2Server) ProcessStreams(handler func(s *Stream)) {
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

		switch frame := frame.(type) {
		case *http2.MetaHeadersFrame:
			if t.handleHeaders(frame, handler) {
				t.Close()
				return
			}

		case *http2.DataFrame:
			t.handleData(frame)
		case *http2.RSTStreamFrame:
			t.handleRSTStream(frame)
		case *http2.SettingsFrame:
			t.handleSettings(frame)
		case *http2.PingFrame:
			t.handlePing(frame)
		case *http2.WindowUpdateFrame:
			t.handleWindowUpdate(frame)
		case *http2.GoAwayFrame:
			// handle goAway in client side
		default:
			log.Info("[transport.http2Server.HandleStreams] not handled frame type: %v", frame)
		}
	}
}

func (t *http2Server) getStream(f http2.Frame) (*Stream, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.activeStreams == nil {
		return nil, false
	}

	v, ok := t.activeStreams[f.Header().StreamID]
	if !ok {
		return nil, false
	}

	return v, true
}

func (t *http2Server) handleHeaders(frame *http2.MetaHeadersFrame, handler func(*Stream)) (close bool) {
	buf := newRecvBuffer()
	// Todo: Add inbound flow control
	s := &Stream{
		id:  frame.Header().StreamID,
		st:  t,
		buf: buf,
		fc:  &inboundFlow{limit: initialWindowSize},
	}

	log.Info("[http2Server.handleHeaders] new stream: %v", s)
	var state decodeState
	for _, hf := range frame.Fields {
		log.Info("[http2Server.handleHeaders] headerFrame: %v", hf)
		state.processHeaderField(hf)
	}

	//Todo: wrtie resetStream control signal to control buf
	if err := state.err; err != nil {
		return
	}

	if frame.StreamEnded() {
		s.state = streamReadDone
	}

	sctx, scancel := context.WithCancel(context.Background())
	if state.timeoutSet {
		sctx, scancel = context.WithTimeout(context.Background(), state.timeout)
	}
	s.ctx, s.cancel = sctx, scancel

	s.ctx = newContextWithStream(s.ctx, s)
	if len(state.mdata) > 0 {
		s.ctx = metadata.NewContext(s.ctx, state.mdata)
	}

	s.reader = &recvBufferReader{
		ctx:  s.ctx,
		recv: s.buf,
	}

	s.method = state.method

	t.mu.Lock()
	if t.state != reachable {
		t.mu.Unlock()
		return false
	}

	// reset stream if active streams large than max streams
	if uint32(len(t.activeStreams)) > t.maxStreams {
		t.mu.Unlock()
		return
	}

	// invalid stream id
	if s.id%2 != 1 || s.id <= t.maxStreamID {
		t.mu.Unlock()
		return true
	}
	t.maxStreamID = s.id
	t.activeStreams[s.id] = s
	t.mu.Unlock()

	handler(s)
	return false
}

func (t *http2Server) handleData(f *http2.DataFrame) {
	size := len(f.Data())
	if err := t.fc.onData(uint32(size)); err != nil {
		log.Info("[http2Server] onData error: %v", err)
		t.Close()
		return
	}

	s, ok := t.getStream(f)
	if !ok {
		if w := t.fc.onRead(uint32(size)); w > 0 {
			t.controlBuf.put(&windowUpdate{0, w})
		}
		return
	}

	if size > 0 {
		s.mu.Lock()
		if s.state == streamDone {
			s.mu.Unlock()
			if wp := t.fc.onRead(uint32(size)); wp > 0 {
				t.controlBuf.put(&windowUpdate{0, wp})
			}
			return
		}

		if err := t.fc.onData(uint32(size)); err != nil {
			s.mu.Unlock()
			t.closeStream(s)
			t.controlBuf.put(&resetStream{s.id, http2.ErrCodeFlowControl})
			return
		}
		s.mu.Unlock()

		data := make([]byte, size)
		copy(data, f.Data())
		s.write(&recvMsg{data: data})
	}

	if f.Header().Flags.Has(http2.FlagDataEndStream) {
		s.mu.Lock()
		if s.state != streamDone {
			s.state = streamReadDone
		}
		s.mu.Unlock()

		s.write(&recvMsg{err: io.EOF})
	}
}

func (t *http2Server) handleRSTStream(f *http2.RSTStreamFrame) {
	s, ok := t.getStream(f)
	if !ok {
		return
	}

	t.closeStream(s)
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

func (t *http2Server) handlePing(f *http2.PingFrame) {
	pingAck := &ping{ack: true}
	copy(pingAck.data[:], f.Data[:])
	t.controlBuf.put(pingAck)
}

func (t *http2Server) handleWindowUpdate(f *http2.WindowUpdateFrame) {
	id := f.Header().StreamID
	// incr := f.Increment
	if id == 0 {
		//todo: add incr to transport sendQuotaPool
		return
	}

	//todo: add incr to sendQuotaPool
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
	t.mu.Lock()
	if t.state == closing {
		t.mu.Unlock()
		return fmt.Errorf("server already closed")
	}

	t.state = closing
	streams := t.activeStreams
	t.activeStreams = nil
	t.mu.Unlock()

	close(t.shutdownCh)
	err := t.conn.Close()
	for _, s := range streams {
		s.cancel()
	}

	return err
}

func (t *http2Server) closeStream(s *Stream) {
	t.mu.Lock()
	delete(t.activeStreams, s.id)
	if t.state == closing && len(t.activeStreams) == 0 {
		defer t.Close()
	}
	t.mu.Unlock()

	s.cancel()
	s.mu.Lock()
	if q := s.fc.resetPendingData(); q > 0 {
		if wp := t.fc.onRead(q); wp > 0 {
			t.controlBuf.put(&windowUpdate{0, wp})
		}
	}

	if s.state == streamDone {
		s.mu.Unlock()
		return
	}

	s.state = streamDone
	s.mu.Unlock()
}
