//go:build unix

package main

import "syscall"

// freeBytes devolve o espaco livre (em bytes) do volume que contem path, ou -1 se nao der para saber.
func freeBytes(path string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return -1
	}
	return int64(st.Bavail) * int64(st.Bsize)
}
