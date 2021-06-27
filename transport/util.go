package transport

import "golang.org/x/net/http2"

var (
	clientPreface = []byte(http2.ClientPreface)
)
