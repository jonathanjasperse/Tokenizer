//go:build !desktopapp

package main

import (
	"errors"
	"log"
)

func runDesktopApplication(bool, bool) error {
	return errors.New("this build does not include the desktop app")
}

func showErrorDialog(title, message string) {
	log.Printf("%s: %s", title, message)
}
