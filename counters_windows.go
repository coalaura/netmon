package main

import (
	"time"

	"golang.org/x/sys/windows"
)

func readInterfaceCounters(state *interfaceState) interfaceCounters {
	result := interfaceCounters{Time: time.Now(), Detail: "No interface counters available"}

	if state.Index == 0 {
		return result
	}

	row := windows.MibIfRow2{InterfaceIndex: uint32(state.Index)}

	err := windows.GetIfEntry2Ex(0, &row)

	result.Time = time.Now()

	if err != nil {
		result.Detail = err.Error()

		return result
	}

	result.Identity = row.InterfaceLuid
	result.RXBytes = row.InOctets
	result.TXBytes = row.OutOctets
	result.RXErrors = row.InErrors
	result.TXErrors = row.OutErrors
	result.RXDiscards = row.InDiscards
	result.TXDiscards = row.OutDiscards
	result.Available = true
	result.Detail = "Windows interface counters"

	return result
}
