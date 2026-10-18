package main

import (
	"encoding/binary"
	"errors"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const tcpTableOwnerPIDAll = 5

var getExtendedTCPTable = windows.NewLazySystemDLL("iphlpapi.dll").NewProc("GetExtendedTcpTable")

func (sampler *connectionSampler) sample() connectionSample {
	result := connectionSample{Time: time.Now(), Detail: "System-wide TCPv4/v6; excludes listening and closed sockets; active excludes TIME_WAIT"}

	if len(sampler.buffer) == 0 {
		sampler.buffer = make([]byte, 16*1024)
	}

	families := [2]uint32{windows.AF_INET, windows.AF_INET6}

	for _, family := range families {
		err := sampler.readTable(family, &result)
		if err != nil {
			if family == windows.AF_INET6 && errors.Is(err, windows.ERROR_NOT_SUPPORTED) {
				continue
			}

			result.Detail = err.Error()

			return result
		}
	}

	result.Available = true
	result.Time = time.Now()

	return result
}

func (sampler *connectionSampler) readTable(family uint32, result *connectionSample) error {
	for range 3 {
		size := uint32(len(sampler.buffer))

		status, _, _ := getExtendedTCPTable.Call(uintptr(unsafe.Pointer(&sampler.buffer[0])), uintptr(unsafe.Pointer(&size)), 0, uintptr(family), tcpTableOwnerPIDAll, 0)
		if status == uintptr(windows.ERROR_INSUFFICIENT_BUFFER) {
			if size == 0 || size > 64*1024*1024 {
				return errors.New("TCP connection table exceeds buffer limit")
			}

			sampler.buffer = make([]byte, size)

			continue
		}

		if status != 0 {
			return windows.Errno(status)
		}

		return countWindowsTCPTable(sampler.buffer, family, result)
	}

	return errors.New("TCP connection table changed repeatedly during collection")
}

func countWindowsTCPTable(data []byte, family uint32, result *connectionSample) error {
	// OWNER_PID tables use DWORD-aligned rows on both architectures: IPv4 24 bytes, IPv6 56.
	rowSize := 24
	stateOffset := 0

	if family == windows.AF_INET6 {
		rowSize = 56
		stateOffset = 48
	}

	if len(data) < 4 {
		return errors.New("truncated TCP connection table")
	}

	count := uint64(binary.LittleEndian.Uint32(data[:4]))
	if count > uint64((len(data)-4)/rowSize) {
		return errors.New("truncated TCP connection rows")
	}

	for index := range int(count) {
		offset := 4 + index*rowSize + stateOffset

		state := binary.LittleEndian.Uint32(data[offset : offset+4])
		if state < 1 || state > 12 {
			return errors.New("unknown TCP connection state")
		}

		if state == 1 || state == 2 {
			continue
		}

		result.Total++

		switch state {
		case 5:
			result.Established++
		case 11:
			result.TimeWait++
		}
	}

	return nil
}
