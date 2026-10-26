package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type instanceInfo struct {
	Port  int    `json:"port"`
	Token string `json:"token"`
	PID   int    `json:"pid"`
}

func instancePath(dir string) string { return filepath.Join(dir, "instance.json") }

func readInstance(dir string) (instanceInfo, bool) {
	var in instanceInfo
	b, err := os.ReadFile(instancePath(dir))
	if err != nil || json.Unmarshal(b, &in) != nil || in.Port == 0 {
		return in, false
	}
	return in, true
}

func instanceCall(in instanceInfo, method, path string) bool {
	req, err := http.NewRequest(method, fmt.Sprintf("http://127.0.0.1:%d%s", in.Port, path), nil)
	if err != nil {
		return false
	}
	req.Header.Set("X-DJ-Token", in.Token)
	c := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := c.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

func setupLogging(path string) {
	if st, err := os.Stat(path); err == nil && st.Size() > 2<<20 {
		_ = os.Remove(path)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	log.SetOutput(io.MultiWriter(os.Stderr, f))
	log.SetFlags(log.LstdFlags)
}

func main() {
	port := flag.Int("port", 0, "porta local (0 = aleatória)")
	noOpen := flag.Bool("no-open", false, "não abrir a janela; só imprimir o endereço")
	keep := flag.Bool("keep", false, "não encerrar sozinho quando a janela fechar")
	showVer := flag.Bool("version", false, "mostrar a versão")
	bg := flag.Bool("detach", false, "rodar em segundo plano, separado de quem chamou")
	flag.Parse()
	if *showVer {
		fmt.Println("DriveCRTV", appVersion)
		return
	}

	dataDir := dataDirPath()
	_ = os.MkdirAll(dataDir, 0o755)
	logPath := filepath.Join(dataDir, "drivecrtv.log")
	if *bg {
		var rest []string
		for _, a := range os.Args[1:] {
			if a != "-detach" && a != "--detach" {
				rest = append(rest, a)
			}
		}
		if err := detach(rest, filepath.Join(dataDir, "drivecrtv-saida.log")); err != nil {
			fmt.Fprintln(os.Stderr, "não consegui iniciar em segundo plano:", err)
			os.Exit(1)
		}
		return
	}
	setupLogging(logPath)

	// instancia unica: se ja esta aberto, so pede uma janela nova ao processo existente
	if in, ok := readInstance(dataDir); ok && instanceCall(in, http.MethodGet, "/api/ping") {
		instanceCall(in, http.MethodPost, "/api/focus")
		log.Println("DriveCRTV já estava rodando; abri uma janela nele.")
		return
	}

	st := newStore(dataDir)
	f := newFetcher(st, defaultEndpoints())
	hub := newHub()
	m := newManager(st, f, dataDir)
	m.loadJobs()
	m.emit = func(v map[string]any) { hub.broadcast(v) }
	m.notify = notifyUser
	m.hasUI = func() bool { return hub.count() > 0 }

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		log.Fatalf("não consegui abrir a porta local: %v", err)
	}
	tb := make([]byte, 16)
	_, _ = rand.Read(tb)
	srv := &Server{st: st, f: f, m: m, hub: hub, token: hex.EncodeToString(tb),
		port: ln.Addr().(*net.TCPAddr).Port, dataDir: dataDir, quit: make(chan struct{}, 1), logPath: logPath}
	httpSrv := &http.Server{Handler: srv.routes(), ReadHeaderTimeout: 15 * time.Second}
	go func() {
		if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("servidor: %v", err)
		}
	}()

	info, _ := json.Marshal(instanceInfo{Port: srv.port, Token: srv.token, PID: os.Getpid()})
	_ = os.WriteFile(instancePath(dataDir), info, 0o600)
	log.Printf("DriveCRTV %s iniciado na porta %d (dados em %s)", appVersion, srv.port, dataDir)

	ctx, cancel := context.WithCancel(context.Background())
	go m.dispatchLoop()
	go m.tickLoop(ctx)
	go srv.broadcastLoop(ctx)
	go srv.clipboardLoop(ctx)
	var once sync.Once
	shutdown := func() {
		once.Do(func() {
			log.Println("encerrando; downloads em andamento ficam pausados para retomar depois")
			cancel()
			m.Shutdown()
			_ = httpSrv.Close()
			if in, ok := readInstance(dataDir); ok && in.PID == os.Getpid() {
				_ = os.Remove(instancePath(dataDir))
			}
		})
		os.Exit(0)
	}

	control := func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		signal.Ignore(syscall.SIGHUP)
		idle := time.NewTicker(2 * time.Second)
		var hiddenSince time.Time
		for {
			select {
			case s := <-sig:
				log.Printf("sinal recebido: %v", s)
				shutdown()
			case <-srv.quit:
				shutdown()
			case <-idle.C:
				if *keep || m.HasActive() {
					hiddenSince = time.Time{}
					continue
				}
				if nativeMode.Load() {
					// app nativo: janela fechada e nada baixando = encerra
					if winVisible.Load() {
						hiddenSince = time.Time{}
						continue
					}
					if hiddenSince.IsZero() {
						hiddenSince = time.Now()
					} else if time.Since(hiddenSince) > 5*time.Second {
						log.Printf("janela fechada e nada baixando; encerrando")
						shutdown()
					}
					continue
				}
				if hub.count() > 0 {
					continue
				}
				since, ever := hub.idle()
				if (ever && since > 12*time.Second) || (!ever && time.Since(hub.started) > 120*time.Second) {
					log.Printf("nenhuma janela aberta e nada baixando; encerrando")
					shutdown()
				}
			}
		}
	}

	if *noOpen {
		fmt.Println(srv.url())
		control()
		return
	}
	if runtime.GOOS == "darwin" {
		nativeMode.Store(true)
		m.hasUI = func() bool { return winVisible.Load() }
		go control()
		err := runNative(srv.url(), m.HasActive, shutdown)
		log.Printf("janela nativa indisponível (%v); usando o navegador", err)
		nativeMode.Store(false)
		m.hasUI = func() bool { return hub.count() > 0 }
		openWindow(srv.url(), dataDir)
		select {}
	}
	go openWindow(srv.url(), dataDir)
	control()
}

// nativeMode indica que a interface roda na janela nativa do macOS.
var nativeMode atomic.Bool
