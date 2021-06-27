package zrpc

import (
	"fmt"
	"io"
	"net"
	"reflect"
	"strings"
	"sync"

	"github.com/zhiruchen/zrpc/transport"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/grpclog"
)

type service struct {
	server    interface{}
	Endpoints map[string]*grpc.MethodDesc
}

type Option func(s *Server)

type serverOptions struct {
	maxConcurrentStreams uint32
	maxRequestMsgSize    int64
	maxRespMsgSize       int64
}

type Server struct {
	opts *serverOptions

	mu    sync.Mutex
	conns map[io.Closer]bool
	svc   map[string]*service
}

func NewServer(opts ...Option) *Server {
	s := &Server{
		opts: &serverOptions{},
	}

	for _, opt := range opts {
		opt(s)
	}

	return s
}

func (s *Server) RegisterService(sd *grpc.ServiceDesc, ss interface{}) {
	ht := reflect.TypeOf(sd.HandlerType).Elem()
	st := reflect.TypeOf(ss)
	if !st.Implements(ht) {
		grpclog.Fatalf("grpc: Server.RegisterService found the handler of type %v that does not satisfy %v", st, ht)
	}
	s.register(sd, ss)
}

func (s *Server) register(sd *grpc.ServiceDesc, ss interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.svc[sd.ServiceName]; ok {
		return
	}

	service := &service{
		server:    ss,
		Endpoints: make(map[string]*grpc.MethodDesc),
	}

	for _, method := range sd.Methods {
		md := method
		service.Endpoints[method.MethodName] = &md
	}
}

func (s *Server) Serve(lis net.Listener) error {
	for {
		conn, err := lis.Accept()
		if err != nil {
			return err
		}

		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(conn net.Conn) {
	st, err := transport.NewServerTransport(conn, s.opts.maxConcurrentStreams)
	if err != nil {
		fmt.Printf("NewServerTransport has error: %v\n", err)
		return
	}

	if !s.addConn(conn) {
		st.Close()
		return
	}

	s.serveStreams(st)
}

func (s *Server) serveStreams(st transport.ServerTransport) {
	defer s.removeConn(st)
	defer st.Close()

	var done = make(chan struct{})
	defer close(done)
	st.ProcessStreams(func(stream *transport.Stream) {
		go func() {
			s.processStream(st, stream)
			done <- struct{}{}
		}()
	})
	<-done
}

func (s *Server) processStream(st transport.ServerTransport, stream *transport.Stream) {
	mp := stream.Method()
	if mp != "" && mp[0] == '/' {
		mp = mp[1:]
	}
	p := strings.LastIndex(mp, "/")
	if p == -1 {
		fmt.Printf("unknow method: %s\n", mp)
		return
	}

	service := mp[:p]
	method := mp[p+1:]
	svc, ok := s.svc[service]
	if !ok {
		fmt.Printf("unknow service: %s\n", service)
		return
	}

	if md, ok := svc.Endpoints[method]; ok {
		s.processUnaryRPC(st, stream, svc, md)
		return
	}

	if err := st.WriteStatus(stream, codes.Unimplemented, "Unknown method "+method); err != nil {
		fmt.Printf("zrpc: Server.processStream failed to write status: %v", err)
	}
}

func (s *Server) processUnaryRPC(st transport.ServerTransport, stream *transport.Stream, srv *service, md *grpc.MethodDesc) {

}

func (s *Server) addConn(c io.Closer) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.conns[c] = true
	return true
}

func (s *Server) removeConn(c io.Closer) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.conns) > 0 {
		delete(s.conns, c)
	}
}
