package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/godbus/dbus/v5"
	"golang.org/x/sys/unix"
)

type linuxRoute struct {
	index        int
	gateway      netip.Addr
	address      netip.Addr
	defaultRoute bool
}

type resolvedServer struct {
	Family  int32
	Address []byte
}

func discoverInterface(ctx context.Context, previous interfaceState) interfaceState {
	state := interfaceState{Link: "missing", ARP: "missing"}

	route, err := lookupRoute(netip.MustParseAddr(ICMPRemoteTargets[0]))
	if err != nil {
		state.Index = previous.Index
		state.Gateway = previous.Gateway
		state.Error = fmt.Sprintf("Route lookup: %v", err)
	} else {
		state.Index = route.index
		state.Gateway = route.gateway
		state.Address = route.address
	}

	if state.Index == 0 {
		adapters, _ := net.Interfaces()

		for index := range adapters {
			adapter := &adapters[index]
			if adapter.Flags&net.FlagLoopback != 0 {
				continue
			}

			state.Index = adapter.Index

			if adapter.Flags&net.FlagUp != 0 {
				break
			}
		}

		if state.Index == 0 {
			return state
		}
	}

	adapter, err := net.InterfaceByIndex(state.Index)
	if err != nil {
		state.Error = fmt.Sprintf("Interface: %v", err)

		return state
	}

	state.Name = adapter.Name
	state.Link = linuxLink(adapter)

	speed, speedError := os.ReadFile("/sys/class/net/" + adapter.Name + "/speed")
	if speedError == nil {
		megabits, parseError := strconv.ParseUint(strings.TrimSpace(string(speed)), 10, 64)
		if parseError == nil && megabits < 1<<32-1 {
			state.Speed = megabits * 1000000
		}
	}

	if !state.Address.IsValid() {
		addresses, addressError := adapter.Addrs()
		if addressError == nil {
			for _, address := range addresses {
				prefix, parseError := netip.ParsePrefix(address.String())
				if parseError == nil && prefix.Addr().Is4() {
					state.Address = prefix.Addr()

					break
				}
			}
		}
	}

	state.DefaultRoute, err = linuxDefaultRoute(state.Index)
	if err != nil {
		state.Error = fmt.Sprintf("Default routes: %v", err)
	}

	state.ARP = linuxNeighbor(state.Index, state.Gateway)
	state.Resolver, state.ResolverSource = linuxResolver(ctx, state.Index)

	return state
}

func lookupRoute(destination netip.Addr) (linuxRoute, error) {
	var route linuxRoute

	descriptor, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_ROUTE)
	if err != nil {
		return route, err
	}

	defer unix.Close(descriptor)

	timeout := unix.NsecToTimeval(probeTimeout.Nanoseconds())

	err = unix.SetsockoptTimeval(descriptor, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &timeout)
	if err != nil {
		return route, err
	}

	var request [36]byte

	binary.NativeEndian.PutUint32(request[:4], uint32(len(request)))
	binary.NativeEndian.PutUint16(request[4:6], unix.RTM_GETROUTE)
	binary.NativeEndian.PutUint16(request[6:8], unix.NLM_F_REQUEST)
	binary.NativeEndian.PutUint32(request[8:12], 1)

	request[16] = unix.AF_INET
	request[17] = 32

	binary.NativeEndian.PutUint16(request[28:30], 8)
	binary.NativeEndian.PutUint16(request[30:32], unix.RTA_DST)

	address := destination.As4()
	copy(request[32:], address[:])

	err = unix.Sendto(descriptor, request[:], 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK})
	if err != nil {
		return route, err
	}

	var response [8192]byte

	length, _, err := unix.Recvfrom(descriptor, response[:], 0)
	if err != nil {
		return route, err
	}

	messages, err := syscall.ParseNetlinkMessage(response[:length])
	if err != nil {
		return route, err
	}

	for index := range messages {
		message := &messages[index]
		if message.Header.Type == unix.NLMSG_ERROR && len(message.Data) >= 4 {
			code := int32(binary.NativeEndian.Uint32(message.Data[:4]))
			if code < 0 {
				return route, syscall.Errno(-code)
			}
		}

		if message.Header.Type == unix.RTM_NEWROUTE {
			return parseLinuxRoute(message)
		}
	}

	return route, fmt.Errorf("no IPv4 route")
}

func parseLinuxRoute(message *syscall.NetlinkMessage) (linuxRoute, error) {
	var route linuxRoute

	if len(message.Data) < unix.SizeofRtMsg {
		return route, fmt.Errorf("short route message")
	}

	route.defaultRoute = message.Data[1] == 0 && message.Data[7] == unix.RTN_UNICAST

	attributes, err := syscall.ParseNetlinkRouteAttr(message)
	if err != nil {
		return route, err
	}

	for _, attribute := range attributes {
		if len(attribute.Value) < 4 {
			continue
		}

		switch attribute.Attr.Type {
		case unix.RTA_OIF:
			route.index = int(binary.NativeEndian.Uint32(attribute.Value))
		case unix.RTA_GATEWAY:
			route.gateway = netip.AddrFrom4([4]byte(attribute.Value[:4]))
		case unix.RTA_PREFSRC:
			route.address = netip.AddrFrom4([4]byte(attribute.Value[:4]))
		}
	}

	return route, nil
}

func linuxDefaultRoute(index int) (bool, error) {
	data, err := syscall.NetlinkRIB(unix.RTM_GETROUTE, unix.AF_INET)
	if err != nil {
		return false, err
	}

	messages, err := syscall.ParseNetlinkMessage(data)
	if err != nil {
		return false, err
	}

	for messageIndex := range messages {
		message := &messages[messageIndex]
		if message.Header.Type != unix.RTM_NEWROUTE {
			continue
		}

		route, parseError := parseLinuxRoute(message)
		if parseError != nil {
			return false, parseError
		}

		if route.index == index && route.defaultRoute {
			return true, nil
		}
	}

	return false, nil
}

func linuxLink(adapter *net.Interface) string {
	if adapter.Flags&net.FlagUp == 0 {
		return "disabled"
	}

	data, err := os.ReadFile("/sys/class/net/" + adapter.Name + "/operstate")
	if err != nil {
		return "unknown"
	}

	state := strings.TrimSpace(string(data))
	if state == "down" {
		return "disconnected"
	}

	if state == "unknown" && adapter.Flags&net.FlagRunning != 0 {
		return "up"
	}

	return state
}

func linuxNeighbor(index int, gateway netip.Addr) string {
	if !gateway.IsValid() {
		return "not applicable"
	}

	data, err := syscall.NetlinkRIB(unix.RTM_GETNEIGH, unix.AF_INET)
	if err != nil {
		return "unknown: " + err.Error()
	}

	messages, err := syscall.ParseNetlinkMessage(data)
	if err != nil {
		return "unknown: " + err.Error()
	}

	for messageIndex := range messages {
		message := &messages[messageIndex]
		if message.Header.Type != unix.RTM_NEWNEIGH || len(message.Data) < 12 {
			continue
		}

		if int(binary.NativeEndian.Uint32(message.Data[4:8])) != index {
			continue
		}

		// ndmsg and rtmsg are both 12 bytes; ParseNetlinkRouteAttr accepts only route types.
		attributesMessage := syscall.NetlinkMessage{Header: syscall.NlMsghdr{Type: unix.RTM_NEWROUTE}, Data: message.Data}

		attributes, parseError := syscall.ParseNetlinkRouteAttr(&attributesMessage)
		if parseError != nil {
			continue
		}

		for _, attribute := range attributes {
			if attribute.Attr.Type == unix.NDA_DST && len(attribute.Value) == 4 && netip.AddrFrom4([4]byte(attribute.Value)) == gateway {
				return neighborState(binary.NativeEndian.Uint16(message.Data[8:10]))
			}
		}
	}

	return "missing"
}

func neighborState(state uint16) string {
	switch {
	case state&unix.NUD_FAILED != 0:
		return "unreachable"
	case state&unix.NUD_INCOMPLETE != 0:
		return "incomplete"
	case state&unix.NUD_REACHABLE != 0:
		return "reachable"
	case state&unix.NUD_STALE != 0:
		return "stale"
	case state&unix.NUD_DELAY != 0:
		return "delay"
	case state&unix.NUD_PROBE != 0:
		return "probe"
	case state&unix.NUD_PERMANENT != 0:
		return "permanent"
	case state&unix.NUD_NOARP != 0:
		return "noarp"
	}

	return "unknown"
}

func linuxResolver(ctx context.Context, index int) (netip.Addr, string) {
	queryContext, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	connection, err := dbus.SystemBus()
	if err == nil {
		path := dbus.ObjectPath("/org/freedesktop/resolve1/link/" + strconv.Itoa(index))
		object := connection.Object("org.freedesktop.resolve1", path)

		var value dbus.Variant

		err = object.CallWithContext(queryContext, "org.freedesktop.DBus.Properties.Get", 0, "org.freedesktop.resolve1.Link", "DNS").Store(&value)
		if err == nil {
			var servers []resolvedServer

			err = dbus.Store([]any{value.Value()}, &servers)
			if err == nil {
				for _, server := range servers {
					if server.Family == unix.AF_INET && len(server.Address) == 4 {
						return netip.AddrFrom4([4]byte(server.Address)), "systemd-resolved link"
					}
				}
			}
		}
	}

	data, err := os.ReadFile("/etc/resolv.conf")
	if err == nil {
		for line := range strings.SplitSeq(string(data), "\n") {
			fields := strings.FieldsSeq(line)

			var isServer bool

			for field := range fields {
				if !isServer {
					if field != "nameserver" {
						break
					}

					isServer = true

					continue
				}

				address, parseError := netip.ParseAddr(field)
				if parseError == nil && address.Is4() {
					return address, "resolv.conf (system fallback)"
				}

				break
			}
		}
	}

	return netip.Addr{}, "unavailable"
}
