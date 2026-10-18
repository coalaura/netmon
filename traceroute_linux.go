package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"golang.org/x/sys/unix"
)

const traceProtocol = "ICMP"

type traceProbe struct {
	source   [4]byte
	target   [4]byte
	protocol int
}

func (*traceProbe) close() {}

func (probe *traceProbe) protocolName() string {
	if probe.protocol == unix.IPPROTO_UDP {
		return "UDP"
	}

	return traceProtocol
}

func (probe *traceProbe) hop(ctx context.Context, ttl int) traceHop {
	result := traceHop{TTL: ttl, Status: statusTimeout, Detail: "No reply within 800ms"}

	if ctx.Err() != nil {
		return result
	}

	// A separate socket per TTL lets the kernel correlate errors, including late replies.
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, probe.protocol)
	if err != nil {
		return linuxTraceError(ttl, err)
	}

	defer unix.Close(fd)

	err = configureTraceSocket(fd, probe.source, ttl)
	if err != nil {
		return linuxTraceError(ttl, err)
	}

	started := time.Now()
	payload := [16]byte{8, 0, 0, 0, 0, 0, 0, byte(ttl), 'n', 'e', 't', 'm', 'o', 'n', 0, byte(ttl)}
	request := payload[:]
	destination := unix.SockaddrInet4{Addr: probe.target}

	if probe.protocol == unix.IPPROTO_UDP {
		request = payload[8:]
		destination.Port = 33434 + ttl
	}

	// Linux ping sockets supply the ICMP identifier and checksum.
	err = unix.Sendto(fd, request, 0, &destination)
	if err != nil {
		return linuxTraceError(ttl, err)
	}

	poll := [1]unix.PollFd{{Fd: int32(fd), Events: unix.POLLERR}}

	if probe.protocol == unix.IPPROTO_ICMP {
		poll[0].Events |= unix.POLLIN
	}

	var reply [64]byte

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

		if poll[0].Revents&unix.POLLERR != 0 {
			hop, received := receiveTraceError(fd, ttl, netip.AddrFrom4(probe.target), time.Since(started), probe.protocol)
			if received {
				return hop
			}
		}

		if poll[0].Revents&unix.POLLIN == 0 || probe.protocol != unix.IPPROTO_ICMP {
			continue
		}

		size, peer, receiveError := unix.Recvfrom(fd, reply[:], unix.MSG_DONTWAIT)
		if errors.Is(receiveError, unix.EAGAIN) || errors.Is(receiveError, unix.EINTR) {
			continue
		}

		if receiveError != nil {
			return linuxTraceError(ttl, receiveError)
		}

		if linuxTraceEchoReply(reply[:size], peer, request, probe.target) {
			return traceHop{TTL: ttl, Address: netip.AddrFrom4(probe.target), Status: statusOK, Duration: time.Since(started), Detail: "Echo reply", Reached: true, Terminal: true}
		}
	}

	return result
}

func openTrace(state *interfaceState, target netip.Addr) (*traceProbe, error) {
	protocol := unix.IPPROTO_ICMP

	// Select the protocol once so the trace and its exported metadata stay consistent.
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, protocol)
	if err == nil {
		unix.Close(fd)
	} else {
		unavailable := errors.Is(err, unix.EACCES) || errors.Is(err, unix.EPERM) || errors.Is(err, unix.EPROTONOSUPPORT) || errors.Is(err, unix.EAFNOSUPPORT)
		if !unavailable {
			return nil, err
		}

		protocol = unix.IPPROTO_UDP
	}

	return &traceProbe{source: state.Address.As4(), target: target.As4(), protocol: protocol}, nil
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

func receiveTraceError(fd, ttl int, target netip.Addr, duration time.Duration, protocol int) (traceHop, bool) {
	var (
		payload [64]byte
		control [128]byte
	)

	_, controlSize, flags, _, err := unix.Recvmsg(fd, payload[:], control[:], unix.MSG_ERRQUEUE|unix.MSG_DONTWAIT)
	if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) {
		return traceHop{}, false
	}

	if err != nil {
		return linuxTraceError(ttl, err), true
	}

	if flags&unix.MSG_CTRUNC != 0 {
		return linuxTraceError(ttl, errors.New("truncated traceroute error control message")), true
	}

	messages, err := unix.ParseSocketControlMessage(control[:controlSize])
	if err != nil {
		return linuxTraceError(ttl, err), true
	}

	for _, message := range messages {
		if message.Header.Level == unix.IPPROTO_IP && message.Header.Type == unix.IP_RECVERR {
			return linuxTraceReply(message.Data, ttl, target, duration, protocol), true
		}
	}

	return traceHop{}, false
}

func linuxTraceEchoReply(reply []byte, peer unix.Sockaddr, request []byte, target [4]byte) bool {
	if len(reply) != len(request) || len(reply) < 8 || reply[0] != 0 || reply[1] != 0 || !bytes.Equal(reply[6:], request[6:]) {
		return false
	}

	if address, ok := peer.(*unix.SockaddrInet4); ok {
		return address.Addr == target
	}

	return false
}

func linuxTraceReply(data []byte, ttl int, target netip.Addr, duration time.Duration, protocol int) traceHop {
	if len(data) < 16 {
		return linuxTraceError(ttl, errors.New("short traceroute extended error"))
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
		result.Detail = "TTL expired in transit"
	} else if protocol == unix.IPPROTO_UDP && data[5] == 3 && data[6] == 3 && result.Address == target {
		result.Status = statusOK
		result.Reached = true
	}

	return result
}

func linuxTraceError(ttl int, err error) traceHop {
	return traceHop{TTL: ttl, Status: errorStatus(err), Detail: err.Error(), Terminal: true}
}
