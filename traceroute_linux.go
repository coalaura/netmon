package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"golang.org/x/sys/unix"
)

const traceProtocol = "UDP"

type traceProbe struct {
	source [4]byte
	target [4]byte
}

func (*traceProbe) close() {}

func (probe *traceProbe) hop(ctx context.Context, ttl int) traceHop {
	result := traceHop{TTL: ttl, Status: statusTimeout, Detail: "No reply within 800ms"}

	// A separate socket per TTL lets the kernel correlate errors, including late replies.
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, unix.IPPROTO_UDP)
	if err != nil {
		return linuxTraceError(ttl, err)
	}

	defer unix.Close(fd)

	err = configureTraceSocket(fd, probe.source, ttl)
	if err != nil {
		return linuxTraceError(ttl, err)
	}

	started := time.Now()
	payload := [8]byte{'n', 'e', 't', 'm', 'o', 'n', 0, byte(ttl)}

	err = unix.Sendto(fd, payload[:], 0, &unix.SockaddrInet4{Addr: probe.target, Port: 33434 + ttl})
	if err != nil {
		return linuxTraceError(ttl, err)
	}

	poll := [1]unix.PollFd{{Fd: int32(fd), Events: unix.POLLERR}}

	var control [128]byte

	deadline := started.Add(probeTimeout)

	for ctx.Err() == nil && time.Now().Before(deadline) {
		remaining := time.Until(deadline)
		wait := max(1, min(50, int(remaining/time.Millisecond)))

		_, err = unix.Poll(poll[:], wait)
		if errors.Is(err, unix.EINTR) {
			continue
		}

		if err != nil {
			return linuxTraceError(ttl, err)
		}

		_, controlSize, flags, _, receiveError := unix.Recvmsg(fd, payload[:], control[:], unix.MSG_ERRQUEUE|unix.MSG_DONTWAIT)
		if errors.Is(receiveError, unix.EAGAIN) || errors.Is(receiveError, unix.EINTR) {
			continue
		}

		if receiveError != nil {
			return linuxTraceError(ttl, receiveError)
		}

		if flags&unix.MSG_CTRUNC != 0 {
			return linuxTraceError(ttl, errors.New("truncated UDP error control message"))
		}

		messages, parseError := unix.ParseSocketControlMessage(control[:controlSize])
		if parseError != nil {
			return linuxTraceError(ttl, parseError)
		}

		for _, message := range messages {
			if message.Header.Level != unix.IPPROTO_IP || message.Header.Type != unix.IP_RECVERR {
				continue
			}

			return linuxTraceReply(message.Data, ttl, netip.AddrFrom4(probe.target), time.Since(started))
		}
	}

	return result
}

func openTrace(state *interfaceState, target netip.Addr) (*traceProbe, error) {
	return &traceProbe{source: state.Address.As4(), target: target.As4()}, nil
}

func configureTraceSocket(fd int, source [4]byte, ttl int) error {
	err := unix.Bind(fd, &unix.SockaddrInet4{Addr: source})
	if err != nil {
		return err
	}

	err = unix.SetsockoptInt(fd, unix.IPPROTO_IP, unix.IP_RECVERR, 1)
	if err != nil {
		return err
	}

	return unix.SetsockoptInt(fd, unix.IPPROTO_IP, unix.IP_TTL, ttl)
}

func linuxTraceReply(data []byte, ttl int, target netip.Addr, duration time.Duration) traceHop {
	if len(data) < 16 {
		return linuxTraceError(ttl, errors.New("short UDP extended error"))
	}

	if data[4] != unix.SO_EE_ORIGIN_ICMP {
		return linuxTraceError(ttl, unix.Errno(binary.NativeEndian.Uint32(data[:4])))
	}

	result := traceHop{TTL: ttl, Status: statusIncomplete, Terminal: true, Detail: fmt.Sprintf("ICMP type %d code %d", data[5], data[6])}

	// sock_extended_err is 16 bytes, followed by the offender's sockaddr_in.
	if len(data) >= 32 && binary.NativeEndian.Uint16(data[16:18]) == unix.AF_INET {
		address := netip.AddrFrom4([4]byte(data[20:24]))
		if !address.IsUnspecified() {
			result.Address = address
			result.Duration = duration
		}
	}

	if data[5] == 11 && data[6] == 0 {
		result.Status = statusOK
		result.Terminal = false
	} else if data[5] == 3 && data[6] == 3 && result.Address == target {
		result.Status = statusOK
		result.Reached = true
	}

	return result
}

func linuxTraceError(ttl int, err error) traceHop {
	return traceHop{TTL: ttl, Status: errorStatus(err), Detail: err.Error(), Terminal: true}
}
