package main

import "time"

type connectionSample struct {
	Time        time.Time
	Total       int
	Established int
	Available   bool
	Detail      string
}

type connectionSampler struct {
	buffer []byte
}
