package transport

import "golang.org/x/net/http2"

type settings struct {
	ack bool
	ss  []http2.Setting
}
