package main

import "golang.org/x/sys/windows"

func openLogFile(path string) error {
	file, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}

	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}

	return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}
