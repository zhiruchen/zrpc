package transport

import (
	"fmt"
	"sync"

	"golang.org/x/net/http2"
)

type windowUpdate struct {
	streamID  uint32
	increment uint32
}

type resetStream struct {
	streamID uint32
	errCode  http2.ErrCode
}

type settings struct {
	ack bool
	ss  []http2.Setting
}

type ping struct {
	ack  bool
	data [8]byte
}

// inboundFlow handle inbound flow control
type inboundFlow struct {
	limit uint32

	mu            sync.Mutex
	pendingData   uint32 // received data size but not consumed by app
	pendingUpdate uint32 // data size app has consumed, but server not send windowupdate for them
}

// onData update pendingData, when received data frame from peer
func (f *inboundFlow) onData(n uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.pendingData += n
	if f.pendingData+f.pendingUpdate > f.limit {
		return fmt.Errorf("received %d bytes, exceeding the limit %d bytes", f.pendingData+f.pendingUpdate, f.limit)
	}

	return nil
}

// onRead returns the window update size to the peer, when app read the data
func (f *inboundFlow) onRead(n uint32) uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.pendingData == 0 {
		return 0
	}

	f.pendingData -= n
	f.pendingUpdate += n
	if f.pendingUpdate >= f.limit/4 {
		wu := f.pendingUpdate
		f.pendingUpdate = 0
		return wu
	}

	return 0
}

func (f *inboundFlow) resetPendingData() uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()

	n := f.pendingData
	f.pendingData = 0
	return n
}
