package main

import "time"

type connectionSample struct {
	Time        time.Time
	Total       int
	Established int
	TimeWait    int
	Available   bool
	Detail      string
}

type connectionSampler struct {
	buffer []byte
}

func (counts *connectionSample) active() int {
	return counts.Total - counts.TimeWait
}
