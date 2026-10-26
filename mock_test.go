package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// mockDrive simula os endpoints do Google que o app usa.
type mockFile struct {
	name         string
	data         []byte
	mime         string
	noRange      bool
	interstitial bool
	quota        bool
	private      bool
	redirect     bool
	md5Override  string
}

type mockFolder struct {
	name string
	kids []string
}

type mockDrive struct {
	srv     *httptest.Server
	mu      sync.Mutex
	files   map[string]*mockFile
	folders map[string]*mockFolder

	apiKey        string
	apiPage       int
	embeddedEmpty bool

	failLeft  int32 // quantas requisicoes de bloco devem falhar com 500
	cutsLeft  int32 // quantas respostas devem ser cortadas no meio
	cutAfter  int64
	bps       atomic.Int64 // bytes/s por conexao (0 = sem limite)
	cur, peak atomic.Int32
	served    atomic.Int64
	sigValid  atomic.Int64
	probes    atomic.Int32
}

func newMockDrive() *mockDrive {
	m := &mockDrive{files: map[string]*mockFile{}, folders: map[string]*mockFolder{}, apiPage: 1000}
	m.sigValid.Store(1)
	mux := http.NewServeMux()
	mux.HandleFunc("/embeddedfolderview", m.handleEmbedded)
	mux.HandleFunc("/drive/folders/", m.handleFolderPage)
	mux.HandleFunc("/download", m.handleDownload)
	mux.HandleFunc("/blob/", m.handleBlob)
	mux.HandleFunc("/document/d/", m.handleDocExport)
	mux.HandleFunc("/drive/v3/files", m.handleAPIList)
	mux.HandleFunc("/drive/v3/files/", m.handleAPIFile)
	m.srv = httptest.NewServer(mux)
	return m
}

func (m *mockDrive) close()               { m.srv.Close() }
func (m *mockDrive) endpoints() Endpoints { u := m.srv.URL; return Endpoints{u, u, u, u} }
func randBytes(n int, seed int64) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	return b
}

func (m *mockDrive) addFile(id, name string, data []byte) *mockFile {
	f := &mockFile{name: name, data: data, mime: "application/octet-stream"}
	m.files[id] = f
	return f
}

func (m *mockDrive) addFolder(id, name string, kids ...string) {
	m.folders[id] = &mockFolder{name: name, kids: kids}
}

// ---------- paginas HTML ----------

func (m *mockDrive) kidsOf(id string) []string {
	if f, ok := m.folders[id]; ok {
		return f.kids
	}
	return nil
}

func (m *mockDrive) handleEmbedded(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if _, ok := m.folders[id]; !ok {
		w.WriteHeader(404)
		fmt.Fprint(w, "<html>not found</html>")
		return
	}
	var b strings.Builder
	b.WriteString(`<html><head><title>Embedded</title></head><body><div class="flip-entries">`)
	if !m.embeddedEmpty {
		for _, k := range m.kidsOf(id) {
			name, mime, href := "", "video/mp4", ""
			if f, ok := m.folders[k]; ok {
				name, mime, href = f.name, "application/vnd.google-apps.folder", "https://drive.google.com/drive/folders/"+k+"?usp=embed_facebook"
			} else if f, ok := m.files[k]; ok {
				name, href = f.name, "https://drive.google.com/file/d/"+k+"/view?usp=embed_facebook"
				if f.mime != "application/octet-stream" && f.mime != "" {
					mime = f.mime
				}
			}
			fmt.Fprintf(&b, `<div class="flip-entry" id="entry-%s" tabindex="0"><div class="flip-entry-list-icon"><a href="%s" target="_blank"><img src="https://drive-thirdparty.googleusercontent.com/16/type/%s"></a></div><div class="flip-entry-info"><a href="%s" target="_blank"><div class="flip-entry-title">%s</div></a><div class="flip-entry-last-modified"><div>Jan 1, 2025</div></div></div></div>`,
				k, href, mime, href, html.EscapeString(name))
		}
	}
	b.WriteString(`</div></body></html>`)
	fmt.Fprint(w, b.String())
}

func jsEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\x22`)
	s = strings.ReplaceAll(s, `'`, `\x27`)
	return strings.ReplaceAll(s, `/`, `\/`)
}

func (m *mockDrive) handleFolderPage(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/drive/folders/")
	fo, ok := m.folders[id]
	if !ok {
		w.WriteHeader(404)
		fmt.Fprint(w, "<html>not found</html>")
		return
	}
	var rows []any
	for _, k := range fo.kids {
		if f, ok := m.folders[k]; ok {
			rows = append(rows, []any{k, []string{id}, f.name, "application/vnd.google-apps.folder"})
		} else if f, ok := m.files[k]; ok {
			rows = append(rows, []any{k, []string{id}, f.name, "video/mp4"})
		}
	}
	js, _ := json.Marshal([]any{rows})
	fmt.Fprintf(w, `<html><head><title>%s - Google Drive</title></head><body><script>window['_DRIVE_ivd'] = '%s';</script></body></html>`,
		html.EscapeString(fo.name), jsEscape(string(js)))
}

// ---------- download sem chave ----------

func parseRange(h string, size int64) (int64, int64, bool) {
	if !strings.HasPrefix(h, "bytes=") {
		return 0, 0, false
	}
	parts := strings.SplitN(strings.TrimPrefix(h, "bytes="), "-", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	var s, e int64
	var err error
	if parts[0] == "" {
		n, _ := strconv.ParseInt(parts[1], 10, 64)
		s, e = size-n, size-1
	} else {
		if s, err = strconv.ParseInt(parts[0], 10, 64); err != nil {
			return 0, 0, false
		}
		e = size - 1
		if parts[1] != "" {
			if e, err = strconv.ParseInt(parts[1], 10, 64); err != nil {
				return 0, 0, false
			}
		}
	}
	if e >= size {
		e = size - 1
	}
	if s > e || s >= size || s < 0 {
		return 0, 0, false
	}
	return s, e, true
}

func (m *mockDrive) serveData(w http.ResponseWriter, r *http.Request, f *mockFile, jsonErrors bool) {
	n := m.cur.Add(1)
	for {
		p := m.peak.Load()
		if n <= p || m.peak.CompareAndSwap(p, n) {
			break
		}
	}
	defer m.cur.Add(-1)
	size := int64(len(f.data))
	h := w.Header()
	h.Set("Accept-Ranges", "bytes")
	h.Set("Content-Type", f.mime)
	h.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, "x", url.PathEscape(f.name)))
	start, end, status := int64(0), size-1, 200
	if rh := r.Header.Get("Range"); rh != "" && !f.noRange {
		s, e, ok := parseRange(rh, size)
		if !ok {
			h.Set("Content-Range", fmt.Sprintf("bytes */%d", size))
			w.WriteHeader(416)
			return
		}
		start, end, status = s, e, 206
		h.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
	}
	if end > start && atomic.AddInt32(&m.failLeft, -1) >= 0 {
		w.WriteHeader(500)
		return
	}
	h.Set("Content-Length", strconv.FormatInt(end-start+1, 10))
	w.WriteHeader(status)
	pos, sent := start, int64(0)
	for pos <= end {
		c := min64(32<<10, end-pos+1)
		if m.cutAfter > 0 && sent >= m.cutAfter && atomic.AddInt32(&m.cutsLeft, -1) >= 0 {
			return // fecha a conexao no meio da resposta
		}
		_, _ = w.Write(f.data[pos : pos+c])
		pos += c
		sent += c
		m.served.Add(c)
		if bps := m.bps.Load(); bps > 0 {
			time.Sleep(time.Duration(float64(c) / float64(bps) * float64(time.Second)))
		}
	}
}

func (m *mockDrive) handleDownload(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	id := q.Get("id")
	f, ok := m.files[id]
	if !ok {
		w.WriteHeader(404)
		fmt.Fprint(w, "<html><body>The file you have requested does not exist</body></html>")
		return
	}
	if r.Header.Get("Range") == "bytes=0-0" {
		m.probes.Add(1)
	}
	switch {
	case f.private:
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(403)
		fmt.Fprint(w, "<html><body>You need access. Request access</body></html>")
	case f.quota:
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<html><body>Sorry, you can't view or download this file at this time. Too many users have viewed or downloaded this file recently.</body></html>")
	case f.interstitial && q.Get("uuid") == "":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<html><body>Google Drive can't scan this file for viruses.<form id="download-form" action="%s/download" method="get"><input type="hidden" name="id" value="%s"><input type="hidden" name="export" value="download"><input type="hidden" name="confirm" value="t"><input type="hidden" name="uuid" value="abc-123"></form></body></html>`, m.srv.URL, id)
	case f.redirect:
		http.Redirect(w, r, fmt.Sprintf("/blob/%s?sig=%d", id, m.sigValid.Load()), http.StatusFound)
	default:
		m.serveData(w, r, f, false)
	}
}

func (m *mockDrive) handleBlob(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/blob/")
	f, ok := m.files[id]
	if !ok {
		w.WriteHeader(404)
		return
	}
	if sig, _ := strconv.ParseInt(r.URL.Query().Get("sig"), 10, 64); sig != m.sigValid.Load() {
		w.WriteHeader(403)
		fmt.Fprint(w, "<html><body>expired</body></html>")
		return
	}
	m.serveData(w, r, f, false)
}

func (m *mockDrive) handleDocExport(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/document/d/"), "/")
	f, ok := m.files[parts[0]]
	if !ok {
		w.WriteHeader(404)
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, f.name))
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
	_, _ = w.Write(f.data)
}

// ---------- API oficial ----------

func apiError(w http.ResponseWriter, status int, reason, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{
		"code": status, "message": msg, "errors": []map[string]string{{"reason": reason}}}})
}

func (m *mockDrive) keyOK(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Query().Get("key") != m.apiKey || m.apiKey == "" {
		apiError(w, 400, "keyInvalid", "API key not valid. Please pass a valid API key.")
		return false
	}
	return true
}

func md5hex(b []byte) string { return fmt.Sprintf("%x", md5Sum(b)) }

func (m *mockDrive) apiMeta(id string) map[string]any {
	if fo, ok := m.folders[id]; ok {
		return map[string]any{"id": id, "name": fo.name, "mimeType": "application/vnd.google-apps.folder"}
	}
	f := m.files[id]
	sum := md5hex(f.data)
	if f.md5Override != "" {
		sum = f.md5Override
	}
	return map[string]any{"id": id, "name": f.name, "mimeType": f.mime, "size": strconv.Itoa(len(f.data)), "md5Checksum": sum}
}

func (m *mockDrive) handleAPIList(w http.ResponseWriter, r *http.Request) {
	if !m.keyOK(w, r) {
		return
	}
	q := r.URL.Query().Get("q")
	i := strings.Index(q, "'")
	j := strings.Index(q[i+1:], "'")
	parent := q[i+1 : i+1+j]
	kids := m.kidsOf(parent)
	sort.Strings(kids)
	off, _ := strconv.Atoi(r.URL.Query().Get("pageToken"))
	endIdx := off + m.apiPage
	if endIdx > len(kids) {
		endIdx = len(kids)
	}
	var files []map[string]any
	for _, k := range kids[off:endIdx] {
		files = append(files, m.apiMeta(k))
	}
	resp := map[string]any{"files": files}
	if endIdx < len(kids) {
		resp["nextPageToken"] = strconv.Itoa(endIdx)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (m *mockDrive) handleAPIFile(w http.ResponseWriter, r *http.Request) {
	if !m.keyOK(w, r) {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/drive/v3/files/")
	if id == "drivecrtv-key-test" {
		apiError(w, 404, "notFound", "File not found")
		return
	}
	f, ok := m.files[id]
	if !ok && m.folders[id] == nil {
		apiError(w, 404, "notFound", "File not found: "+id)
		return
	}
	if r.URL.Query().Get("alt") == "media" {
		if f.quota {
			apiError(w, 403, "downloadQuotaExceeded", "quota")
			return
		}
		m.serveData(w, r, f, true)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(m.apiMeta(id))
}

var _ = bytes.NewReader
