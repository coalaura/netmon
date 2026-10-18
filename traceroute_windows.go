package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	traceProtocol       = "ICMP"
	windowsIPTimeout    = 11010
	windowsIPTTLExpired = 11013
)

// IP_OPTION_INFORMATION uses a native-width pointer, unlike the reply's 32-bit pointers.
type traceIPOptions struct {
	TTL         uint8
	TOS         uint8
	Flags       uint8
	OptionsSize uint8
	OptionsData *byte
}

type traceProbe struct {
	handle  uintptr
	source  uint32
	target  uint32
	options traceIPOptions
	request [8]byte
	reply   [512]byte
}

var (
	createICMP    = windows.NewLazySystemDLL("iphlpapi.dll").NewProc("IcmpCreateFile")
	closeICMP     = windows.NewLazySystemDLL("iphlpapi.dll").NewProc("IcmpCloseHandle")
	sendTraceICMP = windows.NewLazySystemDLL("iphlpapi.dll").NewProc("IcmpSendEcho2Ex")
)

func (probe *traceProbe) close() {
	closeICMP.Call(probe.handle)
}

func (*traceProbe) protocolName() string {
	return traceProtocol
}

func (probe *traceProbe) hop(ctx context.Context, ttl int) traceHop {
	result := traceHop{TTL: ttl, Status: statusTimeout, Detail: "No reply within 800ms"}

	if ctx.Err() != nil {
		return result
	}

	probe.options.TTL = uint8(ttl)
	clear(probe.reply[:])

	started := time.Now()

	replies, _, err := sendTraceICMP.Call(probe.handle, 0, 0, 0, uintptr(probe.source), uintptr(probe.target), uintptr(unsafe.Pointer(&probe.request[0])), uintptr(len(probe.request)), uintptr(unsafe.Pointer(&probe.options)), uintptr(unsafe.Pointer(&probe.reply[0])), uintptr(len(probe.reply)), uintptr(probeTimeout/time.Millisecond))
	if replies == 0 {
		if errors.Is(err, windows.Errno(windowsIPTimeout)) {
			return result
		}

		result.Status = errorStatus(err)
		result.Detail = err.Error()
		result.Terminal = !errors.Is(err, windows.Errno(windowsIPTTLExpired))

		return result
	}

	return windowsTraceReply(probe.reply[:], ttl, time.Since(started))
}

func openTrace(state *interfaceState, target netip.Addr) (*traceProbe, error) {
	handle, _, err := createICMP.Call()
	if handle == ^uintptr(0) {
		return nil, fmt.Errorf("IcmpCreateFile: %w", err)
	}

	sourceBytes := state.Address.As4()
	targetBytes := target.As4()

	return &traceProbe{handle: handle, source: binary.NativeEndian.Uint32(sourceBytes[:]), target: binary.NativeEndian.Uint32(targetBytes[:])}, nil
}

func windowsTraceReply(reply []byte, ttl int, duration time.Duration) traceHop {
	// The first 16 bytes are identical in ICMP_ECHO_REPLY and ICMP_ECHO_REPLY32.
	result := traceHop{TTL: ttl, Status: statusError, Terminal: true, Detail: "Malformed ICMP reply"}

	if len(reply) < 16 {
		return result
	}

	status := binary.NativeEndian.Uint32(reply[4:8])
	if status == windowsIPTimeout {
		result.Status = statusTimeout
		result.Terminal = false
		result.Detail = "No reply within 800ms"

		return result
	}

	address := netip.AddrFrom4([4]byte(reply[:4]))
	if !address.IsUnspecified() {
		result.Address = address
		result.Duration = duration
	}

	result.Detail = fmt.Sprintf("Windows IP status %d", status)

	switch status {
	case 0:
		result.Status = statusOK
		result.Reached = true
		result.Detail = "Echo reply"
	case windowsIPTTLExpired:
		result.Status = statusOK
		result.Terminal = false
		result.Detail = "TTL expired in transit"
	default:
		result.Status = statusIncomplete
	}

	return result
}
