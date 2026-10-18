package main

import (
	"fmt"
	"strings"
	"time"
)

// The sampling goroutine records changes even when the display is frozen or minimized.
// Callers hold monitor.mu while appending events.
func (monitor *monitor) addEvent(timestamp time.Time, message string) {
	message = strings.ReplaceAll(message, "\r", " ")
	message = strings.ReplaceAll(message, "\n", " ")

	monitor.events[monitor.eventNext] = timestamp.Format("15:04:05") + "  " + message
	monitor.eventNext = (monitor.eventNext + 1) % len(monitor.events)
	monitor.eventCount = min(monitor.eventCount+1, len(monitor.events))
}

func (monitor *monitor) recordChanges(current *sample) {
	previous := &monitor.history[(monitor.next+historySize-1)%historySize]
	state := &current.Interface

	if monitor.count == 0 || previous.Interface != *state {
		monitor.addEvent(current.Time, fmt.Sprintf("%s | %s | %s | %s | ARP %s | route %t", state.Name, addressText(state.Address), state.Link, speedText(state.Speed), state.ARP, state.DefaultRoute))
	}

	for index := range current.Probes {
		result := &current.Probes[index]
		if probeStateChanged(result, &previous.Probes[index]) {
			monitor.addEvent(current.Time, probeNames[index]+": "+result.Status+" | "+result.Detail)
		}
	}

	traffic := &current.Traffic
	if traffic.Counters.Available != previous.Traffic.Counters.Available {
		monitor.addEvent(traffic.Counters.Time, "Interface counters: "+traffic.Counters.Detail)
	}

	if traffic.Valid && (traffic.RXErrors > 0 || traffic.TXErrors > 0) {
		monitor.addEvent(traffic.Counters.Time, fmt.Sprintf("Interface counters | errors RX +%d / TX +%d | discards RX +%d / TX +%d", traffic.RXErrors, traffic.TXErrors, traffic.RXDiscards, traffic.TXDiscards))
	}

	if current.Connections.Available != previous.Connections.Available {
		monitor.addEvent(current.Connections.Time, "TCP connection counts: "+current.Connections.Detail)
	}
}

func (monitor *monitor) recordApplication(current *applicationSample) {
	monitor.mu.Lock()
	defer monitor.mu.Unlock()

	previous := monitor.application

	for index := range current.Checks {
		result := &current.Checks[index]
		changed := previous == nil || !sameApplicationNetwork(&previous.Interface, &current.Interface)

		if previous != nil {
			before := &previous.Checks[index]
			changed = changed || before.Status != result.Status || before.Stage != result.Stage || before.HTTPStatus != result.HTTPStatus
		}

		if changed {
			monitor.addEvent(result.Finished, applicationNames[index]+": "+result.Status+" | "+result.Stage+" | "+result.Detail)
		}
	}

	monitor.application = current
	monitor.applications[monitor.applicationNext] = current
	monitor.applicationNext = (monitor.applicationNext + 1) % len(monitor.applications)
	monitor.applicationCount = min(monitor.applicationCount+1, len(monitor.applications))
}
