package main

import "os/exec"

func openLogFile(path string) error {
	return exec.Command("xdg-open", path).Run()
}
