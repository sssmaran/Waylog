package main

import (
	"fmt"
	"os/exec"
	"runtime"
)

func browserCommandFor(goos string) (string, []string) {
	switch goos {
	case "darwin":
		return "open", nil
	case "linux":
		return "xdg-open", nil
	case "windows":
		return "cmd", []string{"/c", "start"}
	default:
		return "", nil
	}
}

func openBrowser(target string) error {
	cmd, prefix := browserCommandFor(runtime.GOOS)
	if cmd == "" {
		return fmt.Errorf("auto-open unsupported on %s", runtime.GOOS)
	}
	args := append([]string(nil), prefix...)
	args = append(args, target)
	return exec.Command(cmd, args...).Start()
}
