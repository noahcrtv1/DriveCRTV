//go:build !darwin

package main

import (
	"fmt"
	"os"
	"os/exec"
)

// Fora do macOS este app so roda em modo de desenvolvimento/teste.

func pickFolder(initial string) (string, error) {
	return os.Getenv("DRIVECRTV_FAKE_PICK"), nil
}

func readClipboard() string { return os.Getenv("DRIVECRTV_FAKE_CLIPBOARD") }

func openWindow(url, dataDir string) {
	fmt.Println("Abra no navegador:", url)
	_ = exec.Command("xdg-open", url).Start()
}

func reveal(path string) {}

func notifyUser(title, msg string) {}

func openLog(p string) { _ = exec.Command("xdg-open", p).Start() }
