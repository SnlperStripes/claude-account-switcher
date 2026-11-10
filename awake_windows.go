package main

import (
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32               = windows.NewLazySystemDLL("kernel32.dll")
	procPowerCreateRequest = kernel32.NewProc("PowerCreateRequest")
	procPowerSetRequest    = kernel32.NewProc("PowerSetRequest")
	procPowerClearRequest  = kernel32.NewProc("PowerClearRequest")
)

// REASON_CONTEXT with a plain reason string.
type reasonContext struct {
	version uint32
	flags   uint32
	reason  *uint16
}

const (
	powerRequestContextSimpleString = 0x1
	powerRequestSystemRequired      = 1
)

// holdAwake keeps the computer from going to sleep until the returned func
// is called. It is a power request like the one Claude's keep awake makes,
// so `powercfg /requests` lists it with reason.
func holdAwake(reason string) (func(), error) {
	r, err := windows.UTF16PtrFromString(reason)
	if err != nil {
		return nil, err
	}
	ctx := reasonContext{flags: powerRequestContextSimpleString, reason: r}
	h, _, err := procPowerCreateRequest.Call(uintptr(unsafe.Pointer(&ctx)))
	runtime.KeepAlive(r)
	if windows.Handle(h) == windows.InvalidHandle {
		return nil, err
	}
	if ok, _, err := procPowerSetRequest.Call(h, powerRequestSystemRequired); ok == 0 {
		windows.CloseHandle(windows.Handle(h))
		return nil, err
	}
	return func() {
		procPowerClearRequest.Call(h, powerRequestSystemRequired)
		windows.CloseHandle(windows.Handle(h))
	}, nil
}
