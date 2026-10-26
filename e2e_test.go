package main

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestServeE2E sobe o servidor completo contra o Google simulado para testes no navegador.
// So roda com DRIVECRTV_E2E=<arquivo onde gravar endereco e hashes>.
func TestServeE2E(t *testing.T) {
	out := os.Getenv("DRIVECRTV_E2E")
	if out == "" {
		t.Skip("defina DRIVECRTV_E2E")
	}
	md := newMockDrive()
	defer md.close()
	md.bps.Store(400 << 10) // 400 KB/s por conexao, para dar tempo de pausar
	sums := map[string]string{}
	add := func(id, name, mime string, size int, seed int64) {
		f := md.addFile(id, name, randBytes(size, seed))
		f.mime = mime
		s := md5.Sum(f.data)
		sums[name] = hex.EncodeToString(s[:])
	}
	add("vidAAAAAAAAAA1", "A001_C002_0912.mov", "video/quicktime", 48<<20, 1)
	add("vidAAAAAAAAAA2", "A001_C010_0912.mov", "video/quicktime", 36<<20, 2)
	add("audAAAAAAAAAA1", "trilha_final.wav", "audio/wav", 6<<20, 3)
	add("imgAAAAAAAAAA1", "still_frame_01.jpg", "image/jpeg", 900<<10, 4)
	add("imgAAAAAAAAAA2", "still_frame_02.jpg", "image/jpeg", 1200<<10, 5)
	add("docAAAAAAAAAA1", "briefing.pdf", "application/pdf", 300<<10, 6)
	md.addFolder("subACAMxxxxxx", "A_CAM", "vidAAAAAAAAAA1", "vidAAAAAAAAAA2")
	md.addFolder("subAUDIOxxxxx", "AUDIO", "audAAAAAAAAAA1")
	md.addFolder("rootPROJxxxxxx", "Projeto_Cliente", "subACAMxxxxxx", "subAUDIOxxxxx", "imgAAAAAAAAAA1", "imgAAAAAAAAAA2", "docAAAAAAAAAA1")

	dataDir := t.TempDir()
	if d := os.Getenv("DRIVECRTV_E2E_DATA"); d != "" {
		dataDir = d
	}
	st := newStore(dataDir)
	st.Update(func(s *Settings) { s.DefaultDest = filepath.Join(dataDir, "Downloads"); s.VerifyMD5 = true })
	f := newFetcher(st, md.endpoints())
	hub := newHub()
	m := newManager(st, f, dataDir)
	m.emit = func(v map[string]any) { hub.broadcast(v) }
	m.notify = func(string, string) {}
	m.hasUI = func() bool { return hub.count() > 0 }
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{st: st, f: f, m: m, hub: hub, token: "e2etoken", port: ln.Addr().(*net.TCPAddr).Port,
		dataDir: dataDir, quit: make(chan struct{}, 1), logPath: filepath.Join(dataDir, "drivecrtv.log")}
	hs := &http.Server{Handler: srv.routes()}
	go hs.Serve(ln)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.dispatchLoop()
	go m.tickLoop(ctx)
	go srv.broadcastLoop(ctx)
	go srv.clipboardLoop(ctx)
	info, _ := json.Marshal(map[string]any{"url": srv.url(), "data": dataDir, "md5": sums})
	_ = os.WriteFile(out, info, 0o644)
	select {
	case <-srv.quit:
	case <-time.After(20 * time.Minute):
	}
	m.Shutdown()
}
