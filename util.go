package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// ---------- erros ----------

const (
	ErrQuota     = "quota"
	ErrPrivate   = "private"
	ErrNotFound  = "notfound"
	ErrRate      = "rate"
	ErrNet       = "network"
	ErrBadKey    = "badkey"
	ErrHTML      = "html"
	ErrDisk      = "disk"
	ErrIntegrity = "integrity"
	ErrNoRange   = "norange"
	ErrExpired   = "expired"
	ErrCanceled  = "canceled"
	ErrOther     = "other"
)

// DriveError e um erro com codigo estavel para a interface e flag de nova tentativa.
type DriveError struct {
	Code      string
	Msg       string
	Retryable bool
}

func (e *DriveError) Error() string { return e.Msg }

func derr(code, msg string, retry bool) *DriveError {
	return &DriveError{Code: code, Msg: msg, Retryable: retry}
}

func errQuota() *DriveError {
	return derr(ErrQuota, "O Google bloqueou este arquivo por excesso de downloads (cota). Costuma liberar em até 24h. Alternativa: faça uma cópia para o seu Drive e baixe de lá.", false)
}

func errPrivate() *DriveError {
	return derr(ErrPrivate, "Este item não é público. O dono precisa compartilhar como \"Qualquer pessoa com o link\".", false)
}

func errNotFound() *DriveError {
	return derr(ErrNotFound, "Item não encontrado. O link está errado ou o arquivo foi removido.", false)
}

// asDriveError normaliza qualquer erro em DriveError.
func asDriveError(err error) *DriveError {
	if err == nil {
		return nil
	}
	var de *DriveError
	if errors.As(err, &de) {
		return de
	}
	if errors.Is(err, context.Canceled) {
		return derr(ErrCanceled, "Cancelado", false)
	}
	if errors.Is(err, syscall.ENOSPC) {
		return derr(ErrDisk, "Disco cheio no destino.", false)
	}
	var pe *os.PathError
	if errors.As(err, &pe) {
		if errors.Is(pe.Err, syscall.ENOSPC) {
			return derr(ErrDisk, "Disco cheio no destino.", false)
		}
		if errors.Is(pe.Err, syscall.EACCES) || errors.Is(pe.Err, syscall.EPERM) {
			return derr(ErrDisk, "Sem permissão para gravar na pasta de destino.", false)
		}
		if _, ok := pe.Err.(syscall.Errno); ok {
			return derr(ErrDisk, "Erro de disco: "+pe.Err.Error(), false)
		}
	}
	return netErr(err)
}

// netErr trata falhas de transporte como temporarias.
func netErr(err error) *DriveError {
	msg := err.Error()
	var ne net.Error
	switch {
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		msg = "conexão encerrada pelo servidor"
	case errors.Is(err, context.DeadlineExceeded):
		msg = "tempo esgotado"
	case errors.As(err, &ne) && ne.Timeout():
		msg = "tempo esgotado"
	}
	return derr(ErrNet, "Falha de rede: "+msg, true)
}

// ---------- tempo ----------

// retryUnit e a unidade dos intervalos entre tentativas (os testes diminuem).
var retryUnit = time.Second

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// ---------- nomes e caminhos ----------

// sanitizeName deixa um nome do Drive seguro como componente de caminho no macOS.
func sanitizeName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '/' || r == 0 || r < 32 {
			return '_'
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	s = strings.TrimRight(s, ". ")
	if s == "" || s == "." || s == ".." {
		return "sem-nome"
	}
	if len(s) > 200 {
		ext := path.Ext(s)
		if len(ext) > 20 {
			ext = ""
		}
		base := s[:len(s)-len(ext)]
		for len(base)+len(ext) > 200 {
			_, size := utf8.DecodeLastRuneInString(base)
			base = base[:len(base)-size]
		}
		s = base + ext
	}
	return s
}

// cleanRel normaliza um caminho relativo "a/b/c.ext" componente por componente.
func cleanRel(p string) string {
	var parts []string
	for _, c := range strings.Split(p, "/") {
		if c == "" || c == "." || c == ".." {
			continue
		}
		parts = append(parts, sanitizeName(c))
	}
	return strings.Join(parts, "/")
}

// uniqueRel acrescenta " (2)", " (3)"... quando dois itens do Drive tem o mesmo caminho.
func uniqueRel(taken map[string]bool, rel string) string {
	try := rel
	ext := path.Ext(rel)
	stem := strings.TrimSuffix(rel, ext)
	for n := 2; taken[strings.ToLower(try)]; n++ {
		try = fmt.Sprintf("%s (%d)%s", stem, n, ext)
	}
	taken[strings.ToLower(try)] = true
	return try
}

func ceilDiv(a, b int64) int64 { return (a + b - 1) / b }

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
