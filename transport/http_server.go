package transport

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net"
	"strconv"
	"sync"

	"github.com/zhiruchen/zrpc/log"
	"github.com/zhiruchen/zrpc/metadata"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
	"google.golang.org/grpc/codes"
)

const (
	http2MaxFrameLen      = 16384 // 16KB per frame
	defaultWindowSize     = 65535
	initialWindowSize     = defaultWindowSize      // for an RPC
	initialConnWindowSize = defaultWindowSize * 16 // for a connection

	zrpcContentType = "application/zrpc"
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

	// sync the write access to the transport
	// get the access by receive a value from the chan, realse the access by sending struct{}{}
	writeableCh chan struct{}

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
		writeableCh:   make(chan struct{}, 1),
		shutdownCh:    make(chan struct{}),
		fc:            &inboundFlow{limit: initialConnWindowSize},
		activeStreams: make(map[uint32]*Stream),
	}

	go h2Server.controller()

	h2Server.writeableCh <- struct{}{}
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
		if se, ok := err.(StreamError); ok {
			t.controlBuf.put(&resetStream{s.id, http2.ErrCode(se.Code)})
		}
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
		t.controlBuf.put(&resetStream{s.id, http2.ErrCodeRefusedStream})
		return
	}

	// invalid stream id
	if s.id%2 != 1 || s.id <= t.maxStreamID {
		t.mu.Unlock()
		log.Info("[tranport] http2Server.ProcessStreams received invalid stream id: %d", s.id)
		return true
	}
	t.maxStreamID = s.id
	t.activeStreams[s.id] = s
	t.mu.Unlock()

	s.windowUpdateHandler = func(n uint32) {
		t.updateWindow(s, n)
	}

	handler(s)
	return false
}

func (t *http2Server) updateWindow(s *Stream, n uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == streamDone {
		return
	}

	if wp := t.fc.onRead(n); wp > 0 {
		t.controlBuf.put(&windowUpdate{0, wp})
	}

	if wp := s.fc.onRead(n); wp > 0 {
		t.controlBuf.put(&windowUpdate{s.id, wp})
	}
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
	s.mu.Lock()
	if s.headerOk || s.state == streamDone {
		s.mu.Unlock()
		return errIllegalHeaderWrite
	}

	s.headerOk = true
	s.mu.Unlock()

	if err := t.waitForWrite(s.ctx); err != nil {
		return err
	}

	t.headerBuf.Reset()
	t.headerEncoder.WriteField(hpack.HeaderField{Name: ":status", Value: "200"})
	t.headerEncoder.WriteField(hpack.HeaderField{Name: "content-type", Value: zrpcContentType})

	for hdr, values := range md {
		if isReservedHeader(hdr) {
			continue
		}

		for _, value := range values {
			t.headerEncoder.WriteField(hpack.HeaderField{Name: hdr, Value: value})
		}
	}

	if err := t.writeHeaders(s, t.headerBuf, false); err != nil {
		return err
	}
	t.writeableCh <- struct{}{}
	return nil
}

func (t *http2Server) writeHeaders(s *Stream, b *bytes.Buffer, endStream bool) error {
	var (
		err         error
		firstHeader = true
		endHeaders  = false
	)

	for !endHeaders {
		size := t.headerBuf.Len()

		if size > http2MaxFrameLen {
			size = http2MaxFrameLen
		} else {
			endHeaders = true
		}

		if firstHeader {
			p := http2.HeadersFrameParam{
				StreamID:      s.id,
				BlockFragment: b.Next(size),
				EndStream:     endStream,
				EndHeaders:    endHeaders,
			}
			err = t.framer.writeHeaders(endHeaders, p)
			firstHeader = false
		} else {
			err = t.framer.writeContinuation(endHeaders, s.id, endHeaders, b.Next(size))
		}

		if err != nil {
			t.Close()
			return ConnectionErrorf(true, err, "[transport]: %v", err)
		}
	}

	return nil
}

func (t *http2Server) Write(s *Stream, data []byte) error {
	var writeHeaderFrame bool

	s.mu.Lock()
	if s.state == streamDone {
		s.mu.Unlock()
		return StreamErrorf(codes.Unknown, "rpc stream has done")
	}

	if !s.headerOk {
		writeHeaderFrame = true
		s.headerOk = true
	}
	s.mu.Unlock()

	if writeHeaderFrame {
		if err := t.waitForWrite(s.ctx); err != nil {
			return err
		}

		t.headerBuf.Reset()
		t.headerEncoder.WriteField(hpack.HeaderField{Name: ":status", Value: "200"})
		t.headerEncoder.WriteField(hpack.HeaderField{Name: "content-type", Value: "application/zrpc"})

		p := http2.HeadersFrameParam{
			StreamID:      s.id,
			BlockFragment: t.headerBuf.Bytes(),
			EndHeaders:    true,
		}
		if err := t.framer.writeHeaders(false, p); err != nil {
			t.Close()
			return ConnectionErrorf(true, err, "transport: %v", err)
		}

		t.writeableCh <- struct{}{}
	}

	r := bytes.NewBuffer(data)
	for {
		if r.Len() == 0 {
			return nil
		}

		p := r.Next(http2MaxFrameLen)
		<-t.writeableCh

		select {
		case <-s.ctx.Done():
			t.writeableCh <- struct{}{}
			return ContextErr(s.ctx.Err())
		default:
		}

		if err := t.framer.writeData(r.Len() == 0, s.id, false, p); err != nil {
			t.Close()
			return ConnectionErrorf(true, err, "transport: %v", err)
		}
		t.writeableCh <- struct{}{}
	}
}

func (t *http2Server) WriteStatus(s *Stream, code codes.Code, desc string) error {
	var headerSent bool

	s.mu.Lock()
	if s.state == streamDone {
		s.mu.Unlock()
		return nil
	}

	if s.headerOk {
		headerSent = true
	}
	s.mu.Unlock()

	if err := t.waitForWrite(s.ctx); err != nil {
		return err
	}

	t.headerBuf.Reset()
	if !headerSent {
		t.headerEncoder.WriteField(hpack.HeaderField{Name: ":status", Value: "200"})
		t.headerEncoder.WriteField(hpack.HeaderField{Name: "content-type", Value: zrpcContentType})
	}
	t.headerEncoder.WriteField(hpack.HeaderField{Name: "rpc-status", Value: strconv.Itoa(int(code))})
	t.headerEncoder.WriteField(hpack.HeaderField{Name: "rpc-message", Value: desc})

	//todo: write trailer metadata to headerBuf

	if err := t.writeHeaders(s, t.headerBuf, true); err != nil {
		t.Close()
		return err
	}

	t.closeStream(s)
	t.writeableCh <- struct{}{}
	return nil
}

func (t *http2Server) controller() {
	for {
		select {
		case v := <-t.controlBuf.get():
			t.controlBuf.load()
			select {
			case <-t.writeableCh:
				switch s := v.(type) {
				case *windowUpdate:
					t.framer.writeWindowUpdate(true, s.streamID, s.increment)
				case *settings:
					if s.ack {
						t.framer.writeSettingsAck(true)
					} else {
						t.framer.writeSettings(true, s.ss...)
					}
				case *resetStream:
					t.framer.writeRSTStream(true, s.streamID, s.errCode)
				case *goAway:
					t.mu.Lock()
					if t.state == closing {
						t.mu.Unlock()
						return
					}
					sid := t.maxStreamID
					t.state = stoping
					t.mu.Unlock()

					t.framer.writeGoAway(true, sid, http2.ErrCodeNo, nil)
				case *ping:
					t.framer.writePing(true, s.ack, s.data)
				case *flushIO:
				default:
					log.Info("[transport.http2Server] controller get not support signal: %v", s)
				}
				t.writeableCh <- struct{}{}
				continue
			case <-t.shutdownCh:
				return
			}
		case <-t.shutdownCh:
			return
		}
	}
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

func (t *http2Server) waitForWrite(ctx context.Context) error {
	_, err := wait(ctx, nil, nil, t.shutdownCh, t.writeableCh)
	return err
}
