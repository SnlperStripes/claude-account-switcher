package main

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procCallNtPowerInformation = windows.NewLazySystemDLL("powrprof.dll").NewProc("CallNtPowerInformation")

// systemRequired reports whether anything keeps the system from sleeping.
func systemRequired(t *testing.T) bool {
	const systemExecutionState, esSystemRequired = 16, 0x1
	var state uint32
	if r, _, _ := procCallNtPowerInformation.Call(systemExecutionState, 0, 0, uintptr(unsafe.Pointer(&state)), unsafe.Sizeof(state)); r != 0 {
		t.Skipf("CallNtPowerInformation: status %#x", r)
	}
	return state&esSystemRequired != 0
}

func TestHoldAwakeKeepsSystemAwake(t *testing.T) {
	if systemRequired(t) {
		t.Skip("something else already keeps the system awake")
	}
	free, err := holdAwake("claude-account-switcher test")
	if err != nil {
		t.Fatal(err)
	}
	if !systemRequired(t) {
		free()
		t.Fatal("system not kept awake while held")
	}
	free()
	if systemRequired(t) {
		t.Fatal("system still kept awake after release")
	}
}
