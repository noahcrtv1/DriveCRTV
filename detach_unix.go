//go:build unix

package main

import (
	"os"
	"os/exec"
	"syscall"
)

// detach reinicia o proprio binario numa sessao nova, separada de quem o chamou
// (o lancador do .app), e grava stdout/stderr no log para registrar qualquer panico.
func detach(args []string, logPath string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	devnull, _ := os.Open(os.DevNull)
	out, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		out = nil
	}
	cmd := exec.Command(self, args...)
	cmd.Stdin = devnull
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Start()
}
