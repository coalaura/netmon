package main

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"time"
)

func (sampler *connectionSampler) sample() connectionSample {
	result := connectionSample{Time: time.Now(), Detail: "Network-namespace TCPv4/v6; excludes listening and closed sockets"}

	if len(sampler.buffer) == 0 {
		sampler.buffer = make([]byte, 16*1024)
	}

	paths := [2]string{"/proc/net/tcp", "/proc/net/tcp6"}

	for index, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			if index == 1 && errors.Is(err, os.ErrNotExist) {
				continue
			}

			result.Detail = err.Error()

			return result
		}

		err = countLinuxTCPTable(file, sampler.buffer, &result)

		file.Close()

		if err != nil {
			result.Detail = err.Error()

			return result
		}
	}

	result.Available = true
	result.Time = time.Now()

	return result
}

func countLinuxTCPTable(reader io.Reader, buffer []byte, result *connectionSample) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(buffer, len(buffer))

	if !scanner.Scan() {
		return errors.Join(errors.New("missing TCP connection table header"), scanner.Err())
	}

	if !bytes.Contains(scanner.Bytes(), []byte("local_address")) {
		return errors.New("invalid TCP connection table header")
	}

	for scanner.Scan() {
		var (
			fieldIndex int
			state      []byte
		)

		for field := range bytes.FieldsSeq(scanner.Bytes()) {
			if fieldIndex == 3 {
				state = field

				break
			}

			fieldIndex++
		}

		if len(state) != 2 {
			return errors.New("malformed TCP connection state")
		}

		numeric := state[1] >= '1' && state[1] <= '9'
		named := state[1] >= 'A' && state[1] <= 'C'

		if state[0] != '0' || !numeric && !named {
			return errors.New("unknown TCP connection state")
		}

		// Linux TCP_LISTEN is 0A, TCP_CLOSE is 07; TIME_WAIT and closing states count.
		if bytes.Equal(state, []byte("0A")) || bytes.Equal(state, []byte("07")) {
			continue
		}

		result.Total++

		if bytes.Equal(state, []byte("01")) {
			result.Established++
		}
	}

	return scanner.Err()
}
