package main

import (
	"encoding/binary"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func readInterfaceCounters(state *interfaceState) interfaceCounters {
	result := interfaceCounters{Time: time.Now(), Identity: uint64(state.Index), Detail: "No 64-bit interface counters available"}

	if state.Index == 0 {
		return result
	}

	data, err := syscall.NetlinkRIB(unix.RTM_GETLINK, unix.AF_UNSPEC)
	if err != nil {
		result.Detail = err.Error()

		return result
	}

	messages, err := syscall.ParseNetlinkMessage(data)
	if err != nil {
		result.Detail = err.Error()

		return result
	}

	for index := range messages {
		message := &messages[index]
		if message.Header.Type != unix.RTM_NEWLINK || len(message.Data) < unix.SizeofIfInfomsg || int(binary.NativeEndian.Uint32(message.Data[4:8])) != state.Index {
			continue
		}

		attributes, parseError := syscall.ParseNetlinkRouteAttr(message)
		if parseError != nil {
			result.Detail = parseError.Error()

			return result
		}

		for _, attribute := range attributes {
			if attribute.Attr.Type != unix.IFLA_STATS64 || len(attribute.Value) < 64 {
				continue
			}

			// rtnl_link_stats64 begins with packet counts, then these six uint64 counters.
			stats := attribute.Value

			result.RXBytes = binary.NativeEndian.Uint64(stats[16:24])
			result.TXBytes = binary.NativeEndian.Uint64(stats[24:32])
			result.RXErrors = binary.NativeEndian.Uint64(stats[32:40])
			result.TXErrors = binary.NativeEndian.Uint64(stats[40:48])
			result.RXDiscards = binary.NativeEndian.Uint64(stats[48:56])
			result.TXDiscards = binary.NativeEndian.Uint64(stats[56:64])
			result.Time = time.Now()
			result.Available = true
			result.Detail = "Linux interface counters"

			return result
		}
	}

	return result
}
