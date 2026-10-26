//go:build darwin

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func appleScriptQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `"`, `\"`)
}

// pickFolder abre o seletor de pastas nativo do macOS. Retorna "" se o usuario cancelar.
func pickFolder(initial string) (string, error) {
	loc := ""
	if st, err := os.Stat(initial); err == nil && st.IsDir() {
		loc = ` default location (POSIX file "` + appleScriptQuote(initial) + `")`
	}
	script := `tell application "System Events"
	activate
	set f to choose folder with prompt "Escolha a pasta de destino"` + loc + `
	return POSIX path of f
end tell`
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "osascript", "-e", script).Output()
	if err != nil {
		return "", nil // cancelado
	}
	p := strings.TrimSpace(string(out))
	if len(p) > 1 {
		p = strings.TrimRight(p, "/")
	}
	return p, nil
}

func readClipboard() string {
	cmd := exec.Command("pbpaste")
	cmd.Env = append(os.Environ(), "LANG=en_US.UTF-8", "LC_ALL=en_US.UTF-8")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	if len(out) > 64<<10 {
		out = out[:64<<10]
	}
	return string(out)
}

func appInstalled(name string) bool {
	home, _ := os.UserHomeDir()
	for _, dir := range []string{"/Applications", filepath.Join(home, "Applications")} {
		if _, err := os.Stat(filepath.Join(dir, name+".app")); err == nil {
			return true
		}
	}
	return false
}

// openWindow abre a interface numa janela propria (modo app do Chrome/Edge/Brave) ou no navegador padrao.
func openWindow(url, dataDir string) {
	profile := filepath.Join(dataDir, "window-profile")
	for _, app := range []string{"Google Chrome", "Microsoft Edge", "Brave Browser", "Chromium", "Arc", "Vivaldi"} {
		if !appInstalled(app) {
			continue
		}
		err := exec.Command("open", "-na", app, "--args",
			"--app="+url,
			"--user-data-dir="+profile,
			"--no-first-run", "--no-default-browser-check",
			"--window-size=1180,800").Start()
		if err == nil {
			return
		}
	}
	_ = exec.Command("open", url).Start()
}

func reveal(path string) {
	if st, err := os.Stat(path); err == nil && !st.IsDir() {
		_ = exec.Command("open", "-R", path).Start()
		return
	}
	_ = exec.Command("open", path).Start()
}

func notifyUser(title, msg string) {
	script := `display notification "` + appleScriptQuote(msg) + `" with title "` + appleScriptQuote(title) + `"`
	_ = exec.Command("osascript", "-e", script).Start()
}

// openLog abre o registro de erros no app Console.
func openLog(p string) { _ = exec.Command("open", "-a", "Console", p).Start() }
