package transport

import (
	"fmt"
	"strconv"
	"time"

	"github.com/zhiruchen/zrpc/log"
	"github.com/zhiruchen/zrpc/metadata"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
	"google.golang.org/grpc/codes"
)

var (
	clientPreface = []byte(http2.ClientPreface)
)

type decodeState struct {
	err      error
	encoding string

	statusCode codes.Code
	statusDesc string

	timeoutSet bool
	timeout    time.Duration
	method     string // rpc method path
	mdata      map[string][]string
}

func isReservedHeader(hdr string) bool {
	if hdr != "" && hdr[0] == ':' {
		return true
	}

	//todo: add reserved headers
	return false
}

func (d *decodeState) processHeaderField(f hpack.HeaderField) {
	switch f.Name {
	case "content-type":
		log.Info("received content type: %v", f.Value)

	case "rpc-encoding":
		d.encoding = f.Value

	case "rpc-status":
		code, err := strconv.Atoi(f.Value)
		if err != nil {

		}
		d.statusCode = codes.Code(code)

	case "rpc-timeout":
		d.timeoutSet = true
		var err error
		d.timeout, err = decodeTimeout(f.Value)
		if err != nil {
			return
		}
	case ":path":
		d.method = f.Value
	default: // handle metadata
		if d.mdata == nil {
			d.mdata = make(map[string][]string)
		}

		k, v, err := metadata.DecodeKeyValue(f.Name, f.Value)
		if err != nil {
			return
		}

		d.mdata[k] = append(d.mdata[k], v)
	}
}

type timeoutUnit uint8

const (
	hour        timeoutUnit = 'H'
	minute      timeoutUnit = 'M'
	second      timeoutUnit = 'S'
	millisecond timeoutUnit = 'm'
	microsecond timeoutUnit = 'u'
	nanosecond  timeoutUnit = 'n'
)

var timeoutUnitHash = map[timeoutUnit]time.Duration{
	hour:        time.Hour,
	minute:      time.Minute,
	second:      time.Second,
	millisecond: time.Millisecond,
	microsecond: time.Microsecond,
	nanosecond:  time.Nanosecond,
}

func decodeTimeout(s string) (time.Duration, error) {
	size := len(s)
	if size < 2 {
		return 0, fmt.Errorf("[transport] timeout string is short: %s", s)
	}

	unit := timeoutUnit(s[size-1])
	if d, ok := timeoutUnitHash[unit]; ok {
		t, err := strconv.ParseInt(s[:size-1], 10, 64)
		if err != nil {
			return 0, err
		}

		return d * time.Duration(t), nil
	}

	return 0, fmt.Errorf("[transport] timeout unit is invalid: %s", s)
}
