//go:build !darwin

package main

import (
	"errors"
	"sync/atomic"
)

var winVisible atomic.Bool

func showWindowAsync() {}

func runNative(string, func() bool, func()) error { return errors.New("janela nativa só no macOS") }
