package zrpc

import "context"

type endpointHandler func(ctx context.Context, req interface{}) (resp interface{}, err error)

type Endpoint struct {
	Name    string
	Handler endpointHandler
}

type service struct {
	Name      string
	Endpoints []Endpoint
}

type Option func(s *Server)

type serverOptions struct {
	maxRequestMsgSize int64
	maxRespMsgSize    int64
}

type Server struct {
	opts    *serverOptions
	service *service
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
