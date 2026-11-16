//go:build desktopapp && !windows

package main

import (
	"errors"
	"fmt"
	"os"
)

func runDesktopApplication(bool, bool) error {
	return errors.New("the desktop app is only available on Windows")
}

func showErrorDialog(title, message string) {
	fmt.Fprintf(os.Stderr, "%s: %s\n", title, message)
}
