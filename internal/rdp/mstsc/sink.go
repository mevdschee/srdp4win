//go:build windows

package mstsc

// The event sink: an IDispatch object written by hand, connected to the
// control's connection point for IMsTscAxEvents. The control calls Invoke
// with the DISPID of the event, on the UI thread.

import (
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"
)

type sinkVtbl struct {
	QueryInterface, AddRef, Release                      uintptr
	GetTypeInfoCount, GetTypeInfo, GetIDsOfNames, Invoke uintptr
}

type sink struct {
	vtbl *sinkVtbl // must be the first field: the object is the interface pointer
	refs int32
	s    *session // nil once the session is gone
}

// live keeps sinks reachable while the control holds pointers to them.
var live sync.Map

var sinkVt sinkVtbl
var sinkVtOnce sync.Once

func newSink(s *session) *sink {
	sinkVtOnce.Do(func() {
		sinkVt = sinkVtbl{
			QueryInterface: syscall.NewCallback(func(this *sink, riid *guid, ppv *uintptr) uintptr {
				if ppv == nil {
					return ePointer
				}
				if riid != nil && (*riid == iidIUnknown || *riid == iidIDispatch || *riid == diidIMsTscAxEvents) {
					*ppv = uintptr(unsafe.Pointer(this))
					atomic.AddInt32(&this.refs, 1)
					return sOK
				}
				*ppv = 0
				return eNoInterface
			}),
			AddRef: syscall.NewCallback(func(this *sink) uintptr { return uintptr(atomic.AddInt32(&this.refs, 1)) }),
			Release: syscall.NewCallback(func(this *sink) uintptr {
				n := atomic.AddInt32(&this.refs, -1)
				if n == 0 {
					live.Delete(this)
				}
				return uintptr(n)
			}),
			GetTypeInfoCount: syscall.NewCallback(func(this *sink, count *uint32) uintptr {
				if count != nil {
					*count = 0
				}
				return sOK
			}),
			GetTypeInfo:   syscall.NewCallback(func(this *sink, index, lcid uint32, info *uintptr) uintptr { return eNotImpl }),
			GetIDsOfNames: syscall.NewCallback(func(this *sink, riid *guid, names *uintptr, n, lcid uint32, ids *int32) uintptr { return eNotImpl }),
			Invoke:        syscall.NewCallback(sinkInvoke),
		}
	})
	k := &sink{vtbl: &sinkVt, refs: 1, s: s}
	live.Store(k, struct{}{})
	return k
}

// release drops our own reference.
func (k *sink) release() {
	if atomic.AddInt32(&k.refs, -1) == 0 {
		live.Delete(k)
	}
}

func sinkInvoke(this *sink, id int32, riid *guid, lcid uint32, flags uint16, dp *dispParams, result *variant, excep, argErr uintptr) (hr uintptr) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("rdp control: event panic", "event", id, "panic", fmt.Sprint(r))
			hr = sOK
		}
	}()
	// The arguments come last to first; all the ones we use are numbers.
	var args []int
	if dp != nil && dp.args != nil && dp.nArgs > 0 && dp.nArgs < 16 {
		raw := unsafe.Slice(dp.args, dp.nArgs)
		args = make([]int, len(raw))
		for i := range raw {
			args[len(raw)-1-i] = raw[i].int()
		}
	}
	// Events that ask a question get the answer that changes nothing.
	if result != nil {
		switch id {
		case evConfirmClose, evReceivedTSPublicKey:
			*result = varBool(true)
		case evAutoReconnecting:
			*result = varInt(0) // autoReconnectContinueAutomatic
		}
	}
	if s := this.s; s != nil {
		s.depth++
		defer func() { s.depth-- }()
		s.event(id, args)
	}
	return sOK
}
