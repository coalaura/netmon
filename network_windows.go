package main

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The field layout mirrors the Windows MIB_IPNET_ROW2 ABI.
type neighborRow struct {
	Address               windows.RawSockaddrInet
	InterfaceIndex        uint32
	InterfaceLuid         uint64
	PhysicalAddress       [32]byte
	PhysicalAddressLength uint32
	State                 uint32
	Flags                 uint8
	Padding               [3]byte
	ReachabilityTime      uint32
}

var (
	getNeighbor  = windows.NewLazySystemDLL("iphlpapi.dll").NewProc("GetIpNetEntry2")
	getBestRoute = windows.NewLazySystemDLL("iphlpapi.dll").NewProc("GetBestRoute2")
)

func discoverInterface(_ context.Context, previous interfaceState) interfaceState {
	state := interfaceState{Link: "missing", ARP: "missing"}

	bestRoute, source, routeError := windowsRoute(netip.MustParseAddr(ICMPRemoteTargets[0]))
	selected := bestRoute.InterfaceIndex

	if routeError != nil {
		selected = uint32(previous.Index)
		state.Error = fmt.Sprintf("Route lookup: %v", routeError)
	}

	buffer := make([]byte, 16384)
	size := uint32(len(buffer))

	var err error

	for range 3 {
		// GetAdaptersAddresses writes a linked list into this caller-owned buffer.
		err = windows.GetAdaptersAddresses(windows.AF_INET, windows.GAA_FLAG_INCLUDE_GATEWAYS|windows.GAA_FLAG_INCLUDE_ALL_INTERFACES, 0, (*windows.IpAdapterAddresses)(unsafe.Pointer(&buffer[0])), &size)
		if !errors.Is(err, windows.ERROR_BUFFER_OVERFLOW) {
			break
		}

		buffer = make([]byte, size)
	}

	if err != nil {
		state.Error = fmt.Sprintf("Adapters: %v", err)

		return state
	}

	first := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buffer[0]))

	if selected == 0 {
		for adapter := first; adapter != nil; adapter = adapter.Next {
			if adapter.IfType != 6 && adapter.IfType != 71 {
				continue
			}

			selected = adapter.IfIndex

			if adapter.OperStatus == 1 {
				break
			}
		}
	}

	for adapter := first; adapter != nil; adapter = adapter.Next {
		if adapter.IfIndex != selected {
			continue
		}

		state.Index = int(adapter.IfIndex)
		state.Name = windows.UTF16PtrToString(adapter.FriendlyName)
		state.Link = windowsLink(adapter)
		state.Speed = adapter.TransmitLinkSpeed
		state.ResolverSource = "adapter"

		if adapter.FirstGatewayAddress != nil {
			gateway, valid := netip.AddrFromSlice(adapter.FirstGatewayAddress.Address.IP())
			if valid && gateway.Unmap().Is4() {
				state.Gateway = gateway.Unmap()
			}
		}

		for address := adapter.FirstUnicastAddress; address != nil; address = address.Next {
			parsed, valid := netip.AddrFromSlice(address.Address.IP())
			if valid && parsed.Unmap().Is4() && !parsed.IsUnspecified() {
				state.Address = parsed.Unmap()

				break
			}
		}

		for server := adapter.FirstDnsServerAddress; server != nil; server = server.Next {
			parsed, valid := netip.AddrFromSlice(server.Address.IP())
			if valid && parsed.Unmap().Is4() {
				state.Resolver = parsed.Unmap()

				break
			}
		}

		break
	}

	if routeError == nil {
		state.Address = source

		address := (*windows.RawSockaddrInet4)(unsafe.Pointer(&bestRoute.NextHop))

		state.Gateway = netip.AddrFrom4(address.Addr)
		if state.Gateway.IsUnspecified() {
			state.Gateway = netip.Addr{}
		}
	}

	var routes *windows.MibIpForwardTable2

	err = windows.GetIpForwardTable2(windows.AF_INET, &routes)
	if err != nil {
		state.Error = fmt.Sprintf("Routes: %v", err)

		return state
	}

	defer windows.FreeMibTable(unsafe.Pointer(routes))

	bestMetric := ^uint32(0)
	rows := routes.Rows()

	for index := range rows {
		route := &rows[index]
		if route.InterfaceIndex != selected || route.DestinationPrefix.PrefixLength != 0 || route.Metric >= bestMetric {
			continue
		}

		state.DefaultRoute = true
		bestMetric = route.Metric

		if routeError != nil {
			address := (*windows.RawSockaddrInet4)(unsafe.Pointer(&route.NextHop))

			state.Gateway = netip.AddrFrom4(address.Addr)
			if state.Gateway.IsUnspecified() {
				state.Gateway = netip.Addr{}
			}
		}
	}

	if routeError != nil && !state.Gateway.IsValid() && state.Index == previous.Index && !state.DefaultRoute {
		state.Gateway = previous.Gateway
	}

	state.ARP = windowsNeighbor(state.Index, state.Gateway)

	return state
}

func windowsRoute(target netip.Addr) (windows.MibIpForwardRow2, netip.Addr, error) {
	var (
		destination windows.RawSockaddrInet
		source      windows.RawSockaddrInet
		route       windows.MibIpForwardRow2
	)

	address := (*windows.RawSockaddrInet4)(unsafe.Pointer(&destination))
	address.Family = windows.AF_INET
	address.Addr = target.As4()

	result, _, _ := getBestRoute.Call(0, 0, 0, uintptr(unsafe.Pointer(&destination)), 0, uintptr(unsafe.Pointer(&route)), uintptr(unsafe.Pointer(&source)))
	if result != 0 {
		return route, netip.Addr{}, windows.Errno(result)
	}

	local := (*windows.RawSockaddrInet4)(unsafe.Pointer(&source))
	return route, netip.AddrFrom4(local.Addr), nil
}

func windowsLink(adapter *windows.IpAdapterAddresses) string {
	row := windows.MibIfRow2{InterfaceIndex: adapter.IfIndex}

	err := windows.GetIfEntry2Ex(0, &row)
	if err == nil {
		if row.AdminStatus == 2 {
			return "disabled"
		}

		if row.MediaConnectState == 2 {
			return "disconnected"
		}
	}

	switch adapter.OperStatus {
	case 1:
		return "up"
	case 2:
		return "down"
	case 5:
		return "dormant"
	case 6:
		return "missing"
	case 7:
		return "lower layer down"
	}

	return "unknown"
}

func windowsNeighbor(index int, gateway netip.Addr) string {
	if !gateway.IsValid() {
		return "not applicable"
	}

	row := neighborRow{InterfaceIndex: uint32(index)}

	address := (*windows.RawSockaddrInet4)(unsafe.Pointer(&row.Address))
	address.Family = windows.AF_INET
	address.Addr = gateway.As4()

	result, _, _ := getNeighbor.Call(uintptr(unsafe.Pointer(&row)))
	if result == uintptr(windows.ERROR_NOT_FOUND) {
		return "missing"
	}

	if result != 0 {
		return fmt.Sprintf("unknown (%d)", result)
	}

	states := [...]string{"unreachable", "incomplete", "probe", "delay", "stale", "reachable", "permanent"}
	if int(row.State) >= len(states) {
		return "unknown"
	}

	return states[row.State]
}
