package main

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

//go:embed web
var webFS embed.FS

type Server struct {
	st      *Store
	f       *Fetcher
	m       *Manager
	hub     *Hub
	token   string
	port    int
	dataDir string
	quit    chan struct{}
	logPath string
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<20)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil && err != io.EOF {
		writeJSON(w, 400, map[string]any{"ok": false, "error": "requisição inválida"})
		return false
	}
	return true
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(webFS, "web")
	static := http.FileServer(http.FS(sub))
	mux.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		static.ServeHTTP(w, r)
	}))
	mux.HandleFunc("GET /api/ping", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "version": appVersion})
	})
	mux.HandleFunc("GET /api/state", s.handleState)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("POST /api/scan", s.handleScan)
	mux.HandleFunc("POST /api/probe", s.handleProbe)
	mux.HandleFunc("POST /api/download", s.handleDownload)
	mux.HandleFunc("POST /api/jobs/clear", func(w http.ResponseWriter, r *http.Request) {
		s.m.ClearFinished()
		writeJSON(w, 200, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /api/jobs/{id}/{action}", s.handleJobAction)
	mux.HandleFunc("GET /api/jobs/{id}/items", func(w http.ResponseWriter, r *http.Request) {
		j := s.m.findJob(r.PathValue("id"))
		if j == nil {
			writeJSON(w, 404, map[string]any{"ok": false})
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "items": j.itemViews()})
	})
	mux.HandleFunc("POST /api/settings", s.handleSettings)
	mux.HandleFunc("POST /api/test-key", s.handleTestKey)
	mux.HandleFunc("POST /api/pick-folder", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Initial string `json:"initial"`
		}
		if !readJSON(w, r, &req) {
			return
		}
		p, _ := pickFolder(req.Initial)
		writeJSON(w, 200, map[string]any{"ok": true, "path": p})
	})
	mux.HandleFunc("POST /api/reveal", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Path string `json:"path"`
		}
		if !readJSON(w, r, &req) {
			return
		}
		if filepath.IsAbs(req.Path) {
			reveal(req.Path)
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	})
	mux.HandleFunc("GET /api/free", func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Query().Get("path")
		for p != "" && p != "/" {
			if _, err := os.Stat(p); err == nil {
				break
			}
			p = filepath.Dir(p)
		}
		writeJSON(w, 200, map[string]any{"ok": true, "free": freeBytes(p)})
	})
	mux.HandleFunc("POST /api/focus", func(w http.ResponseWriter, r *http.Request) {
		if nativeMode.Load() {
			showWindowAsync()
		} else {
			go openWindow(s.url(), s.dataDir)
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /api/quit", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true})
		go func() {
			time.Sleep(200 * time.Millisecond)
			select {
			case s.quit <- struct{}{}:
			default:
			}
		}()
	})
	mux.HandleFunc("POST /api/open-log", func(w http.ResponseWriter, r *http.Request) {
		openLog(s.logPath)
		writeJSON(w, 200, map[string]any{"ok": true})
	})
	mux.HandleFunc("GET /api/log", func(w http.ResponseWriter, r *http.Request) {
		b, _ := os.ReadFile(s.logPath)
		if len(b) > 200<<10 {
			b = b[len(b)-200<<10:]
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write(b)
	})
	return s.guard(mux)
}

func (s *Server) url() string { return fmt.Sprintf("http://127.0.0.1:%d/?t=%s", s.port, s.token) }

func (s *Server) tokenOK(v string) bool {
	return v != "" && subtle.ConstantTimeCompare([]byte(v), []byte(s.token)) == 1
}

func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != fmt.Sprintf("127.0.0.1:%d", s.port) && r.Host != fmt.Sprintf("localhost:%d", s.port) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/" && s.tokenOK(r.URL.Query().Get("t")) {
			http.SetCookie(w, &http.Cookie{Name: "dj", Value: s.token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		ok := s.tokenOK(r.Header.Get("X-DJ-Token"))
		if c, err := r.Cookie("dj"); err == nil && s.tokenOK(c.Value) {
			ok = true
		}
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if o := r.Header.Get("Origin"); o != "" && o != "http://"+r.Host {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// ---------- estado e eventos ----------

func (s *Server) stateEvent() map[string]any {
	return map[string]any{
		"type":   "state",
		"jobs":   s.m.Snapshot(),
		"speed":  s.m.GlobalSpeed(),
		"keyBad": s.f.apiBad.Load(),
	}
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	home, _ := os.UserHomeDir()
	ev := s.stateEvent()
	ev["version"] = appVersion
	ev["settings"] = s.st.Get()
	ev["platform"] = runtime.GOOS
	ev["home"] = home
	writeJSON(w, 200, ev)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "sem suporte a streaming", 500)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	ch := s.hub.subscribe()
	defer s.hub.unsubscribe(ch)
	send := func(b []byte) bool {
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			return false
		}
		fl.Flush()
		return true
	}
	first, _ := json.Marshal(s.stateEvent())
	if !send(first) {
		return
	}
	ka := time.NewTicker(15 * time.Second)
	defer ka.Stop()
	for {
		select {
		case b := <-ch:
			if !send(b) {
				return
			}
		case <-ka.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			fl.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) broadcastLoop(ctx context.Context) {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if s.hub.count() > 0 {
				s.hub.broadcast(s.stateEvent())
			}
		}
	}
}

func (s *Server) clipboardLoop(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	last := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if !s.st.Get().WatchClipboard || s.hub.count() == 0 {
			continue
		}
		txt := readClipboard()
		if txt == last {
			continue
		}
		last = txt
		links := parseLinks(txt)
		if len(links) == 0 {
			continue
		}
		if len(txt) > 8000 {
			txt = txt[:8000]
		}
		s.hub.broadcast(map[string]any{"type": "clipboard", "text": txt, "count": len(links)})
	}
}

// ---------- analise de links ----------

func shortLink(l Link) string {
	if len(l.ID) > 8 {
		return string(l.Kind) + " " + l.ID[:8] + "…"
	}
	return string(l.Kind) + " " + l.ID
}

func (s *Server) handleScan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text string `json:"text"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	links := parseLinks(req.Text)
	if len(links) == 0 {
		writeJSON(w, 200, map[string]any{"ok": false, "error": "Não encontrei nenhum link do Google Drive nesse texto."})
		return
	}
	if len(links) > 20 {
		links = links[:20]
	}
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Minute)
	defer cancel()
	var lastEmit atomic.Int64
	prog := func(count int, current string) {
		now := time.Now().UnixMilli()
		if now-lastEmit.Load() < 250 {
			return
		}
		lastEmit.Store(now)
		s.hub.broadcast(map[string]any{"type": "scan", "count": count, "current": current})
	}
	var roots []*Node
	var warns []string
	for _, l := range links {
		var n *Node
		var err error
		if l.Kind == KindFolder {
			n, err = s.f.BuildFolder(ctx, l, prog)
		} else {
			n, err = s.f.Describe(ctx, l)
		}
		if err != nil {
			log.Printf("scan %s: %v", shortLink(l), err)
			warns = append(warns, shortLink(l)+": "+asDriveError(err).Msg)
			continue
		}
		roots = append(roots, n)
	}
	if len(roots) == 0 {
		writeJSON(w, 200, map[string]any{"ok": false, "error": strings.Join(warns, "\n")})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "roots": roots, "warnings": warns, "hasKey": s.f.apiKey() != ""})
}

func (s *Server) handleProbe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Items []struct {
			ID          string `json:"id"`
			Kind        string `json:"kind"`
			ResourceKey string `json:"resourceKey"`
		} `json:"items"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.Items) > 60 {
		req.Items = req.Items[:60]
	}
	type result struct {
		Size int64  `json:"size"`
		MD5  string `json:"md5,omitempty"`
		Err  string `json:"err,omitempty"`
	}
	out := map[string]result{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 6)
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	for _, it := range req.Items {
		it := it
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			res, err := s.f.Resolve(ctx, ItemRef{ID: it.ID, Kind: it.Kind, ResourceKey: it.ResourceKey, Size: -1})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				out[it.ID] = result{Size: -1, Err: asDriveError(err).Msg}
				return
			}
			out[it.ID] = result{Size: res.Size, MD5: res.MD5}
		}()
	}
	wg.Wait()
	writeJSON(w, 200, map[string]any{"ok": true, "results": out})
}

// ---------- downloads ----------

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	var req AddReq
	if !readJSON(w, r, &req) {
		return
	}
	req.Dest = strings.TrimSpace(req.Dest)
	if err := os.MkdirAll(req.Dest, 0o755); err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": "Não consegui usar essa pasta de destino: " + err.Error()})
		return
	}
	j, err := s.m.AddJob(req)
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	log.Printf("job %s criado: %q, %d itens, destino %s", j.ID, j.Title, len(j.items), j.DestRoot)
	writeJSON(w, 200, map[string]any{"ok": true, "id": j.ID})
}

func (s *Server) handleJobAction(w http.ResponseWriter, r *http.Request) {
	id, action := r.PathValue("id"), r.PathValue("action")
	ok := false
	switch action {
	case "pause":
		ok = s.m.Pause(id)
	case "resume":
		ok = s.m.Resume(id)
	case "cancel":
		ok = s.m.Cancel(id)
	case "remove":
		ok = s.m.Remove(id)
	}
	writeJSON(w, 200, map[string]any{"ok": ok})
}

// ---------- ajustes ----------

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	var p struct {
		APIKey         *string        `json:"apiKey"`
		ConnsPerFile   *int           `json:"connsPerFile"`
		MaxConns       *int           `json:"maxConns"`
		ParallelFiles  *int           `json:"parallelFiles"`
		ChunkMB        *int           `json:"chunkMB"`
		VerifyMD5      *bool          `json:"verifyMD5"`
		WatchClipboard *bool          `json:"watchClipboard"`
		Subfolder      *bool          `json:"subfolder"`
		DefaultDest    *string        `json:"defaultDest"`
		Destinations   *[]Destination `json:"destinations"`
	}
	if !readJSON(w, r, &p) {
		return
	}
	cur := s.st.Update(func(c *Settings) {
		if p.APIKey != nil {
			c.APIKey = *p.APIKey
		}
		if p.ConnsPerFile != nil {
			c.ConnsPerFile = *p.ConnsPerFile
		}
		if p.MaxConns != nil {
			c.MaxConns = *p.MaxConns
		}
		if p.ParallelFiles != nil {
			c.ParallelFiles = *p.ParallelFiles
		}
		if p.ChunkMB != nil {
			c.ChunkMB = *p.ChunkMB
		}
		if p.VerifyMD5 != nil {
			c.VerifyMD5 = *p.VerifyMD5
		}
		if p.WatchClipboard != nil {
			c.WatchClipboard = *p.WatchClipboard
		}
		if p.Subfolder != nil {
			c.Subfolder = *p.Subfolder
		}
		if p.DefaultDest != nil && filepath.IsAbs(*p.DefaultDest) {
			c.DefaultDest = *p.DefaultDest
		}
		if p.Destinations != nil {
			var clean []Destination
			for _, d := range *p.Destinations {
				if strings.TrimSpace(d.Name) != "" && filepath.IsAbs(d.Path) {
					clean = append(clean, Destination{Name: strings.TrimSpace(d.Name), Path: d.Path})
				}
			}
			c.Destinations = clean
		}
	})
	if p.APIKey != nil {
		s.f.apiBad.Store(false)
	}
	s.m.applySettings()
	writeJSON(w, 200, map[string]any{"ok": true, "settings": cur})
}

func (s *Server) handleTestKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key string `json:"key"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	key := strings.TrimSpace(req.Key)
	if key == "" {
		writeJSON(w, 200, map[string]any{"ok": false, "error": "Cole a chave primeiro."})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	q := url.Values{"key": {key}, "fields": {"id"}}
	hreq, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.f.ep.API+"/drive/v3/files/drivecrtv-key-test?"+q.Encode(), nil)
	setCommon(hreq)
	resp, err := s.f.client().Do(hreq)
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": asDriveError(err).Msg})
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	// 404 "notFound" significa: chave valida e Drive API ativa (o arquivo de teste nao existe).
	if resp.StatusCode == 404 || resp.StatusCode == 200 {
		writeJSON(w, 200, map[string]any{"ok": true})
		return
	}
	e := mapAPIError(resp.StatusCode, body)
	writeJSON(w, 200, map[string]any{"ok": false, "error": e.Msg})
}
