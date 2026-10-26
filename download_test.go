package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func md5Sum(b []byte) [16]byte { return md5.Sum(b) }

func TestMain(m *testing.M) {
	retryUnit = 5 * time.Millisecond
	os.Exit(m.Run())
}

type env struct {
	t    *testing.T
	mock *mockDrive
	st   *Store
	f    *Fetcher
	m    *Manager
	dd   string
	dest string
}

func newEnv(t *testing.T, mut func(*Settings)) *env {
	t.Helper()
	mock := newMockDrive()
	dd := t.TempDir()
	st := newStore(dd)
	st.Update(func(s *Settings) {
		s.ConnsPerFile, s.MaxConns, s.ParallelFiles, s.ChunkMB = 8, 16, 4, 4
		s.APIKey, s.VerifyMD5 = "", true
		if mut != nil {
			mut(s)
		}
	})
	f := newFetcher(st, mock.endpoints())
	m := newManager(st, f, dd)
	ctx, cancel := context.WithCancel(context.Background())
	go m.dispatchLoop()
	go m.tickLoop(ctx)
	t.Cleanup(func() { cancel(); m.Shutdown(); mock.close() })
	return &env{t: t, mock: mock, st: st, f: f, m: m, dd: dd, dest: t.TempDir()}
}

func fileItem(id, name string, size int64) AddItem {
	return AddItem{ID: id, Name: name, Path: name, Kind: "file", Size: size}
}

func (e *env) job(title string, items ...AddItem) *Job {
	e.t.Helper()
	j, err := e.m.AddJob(AddReq{Title: title, Dest: e.dest, Items: items})
	if err != nil {
		e.t.Fatal(err)
	}
	return j
}

func (e *env) wait(j *Job, d time.Duration, states ...string) JobView {
	e.t.Helper()
	if len(states) == 0 {
		states = []string{"done", "failed", "canceled"}
	}
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		v := j.view()
		for _, s := range states {
			if v.State == s {
				return v
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.t.Fatalf("timeout esperando %v; estado: %+v itens: %+v", states, j.view(), j.itemViews())
	return JobView{}
}

func (e *env) read(rel string) []byte {
	e.t.Helper()
	b, err := os.ReadFile(filepath.Join(e.dest, rel))
	if err != nil {
		e.t.Fatal(err)
	}
	return b
}

func (e *env) mustDone(j *Job) {
	e.t.Helper()
	v := e.wait(j, 40*time.Second)
	if v.State != "done" {
		e.t.Fatalf("job terminou em %q: %s | %+v", v.State, v.Err, j.itemViews())
	}
}

// ---------- links e parsers ----------

func TestParseLinks(t *testing.T) {
	const id = "1AbCdEfGhIjKlMnOpQrStUvWxYz"
	cases := []struct {
		in   string
		kind LinkKind
		rk   string
	}{
		{"https://drive.google.com/file/d/" + id + "/view?usp=sharing", KindFile, ""},
		{"https://drive.google.com/drive/folders/" + id + "?usp=sharing", KindFolder, ""},
		{"https://drive.google.com/drive/u/2/folders/" + id, KindFolder, ""},
		{"https://drive.google.com/open?id=" + id, KindFile, ""},
		{"https://drive.google.com/uc?export=download&id=" + id, KindFile, ""},
		{"https://drive.google.com/drive/folders/" + id + "?resourcekey=0-xYz123&usp=sharing", KindFolder, "0-xYz123"},
		{"https://docs.google.com/document/d/" + id + "/edit", KindDoc, ""},
		{"https://docs.google.com/spreadsheets/d/" + id + "/edit#gid=0", KindSheet, ""},
		{"https://docs.google.com/presentation/d/" + id + "/edit", KindSlide, ""},
	}
	for _, c := range cases {
		ls := parseLinks(c.in)
		if len(ls) != 1 || ls[0].Kind != c.kind || ls[0].ID != id || ls[0].ResourceKey != c.rk {
			t.Errorf("%s -> %+v", c.in, ls)
		}
	}
	mixed := "olha: https://drive.google.com/file/d/" + id + "/view, e também\nhttps://drive.google.com/drive/folders/" + id + "\nhttps://drive.google.com/file/d/" + id + "/view?usp=drive_link"
	if ls := parseLinks(mixed); len(ls) != 2 {
		t.Errorf("mixed: %+v", ls)
	}
	for _, bad := range []string{"https://example.com/file/d/" + id, "https://drive.google.com/drive/my-drive",
		"https://docs.google.com/forms/d/" + id + "/edit", "sem link nenhum"} {
		if ls := parseLinks(bad); len(ls) != 0 {
			t.Errorf("deveria ignorar %q -> %+v", bad, ls)
		}
	}
}

func TestSanitize(t *testing.T) {
	if got := sanitizeName("a/b:c"); got != "a_b:c" {
		t.Error(got)
	}
	if got := sanitizeName(".."); got != "sem-nome" {
		t.Error(got)
	}
	if got := cleanRel("../x//y/./z.mov"); got != "x/y/z.mov" {
		t.Error(got)
	}
	long := strings.Repeat("é", 150) + ".mov"
	if got := sanitizeName(long); len(got) > 200 || !strings.HasSuffix(got, ".mov") {
		t.Error(len(got), got)
	}
	taken := map[string]bool{}
	a, b, c := uniqueRel(taken, "x/A.mov"), uniqueRel(taken, "x/a.mov"), uniqueRel(taken, "x/A.mov")
	if a != "x/A.mov" || b != "x/a (2).mov" || c != "x/A (3).mov" {
		t.Error(a, b, c)
	}
}

func TestUnescapeJS(t *testing.T) {
	in := `[[\x22id1\x22,\x22café \\ ok\x22]]\/x`
	want := `[[` + `"id1","café \ ok"` + `]]/x`
	if got := unescapeJS(in); got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestParseEmbedded(t *testing.T) {
	page := `<div class="flip-entry" id="entry-ABCDEFGHIJ1"><a href="https://drive.google.com/file/d/ABCDEFGHIJ1/view"><img src="https://drive-thirdparty.googleusercontent.com/16/type/video/quicktime"></a><div class="flip-entry-title">Take 01 &amp; 02.mov</div></div>
<div class="flip-entry" id="entry-ABCDEFGHIJ2"><a href="https://drive.google.com/drive/folders/ABCDEFGHIJ2"><img src="https://drive-thirdparty.googleusercontent.com/16/type/application/vnd.google-apps.folder"></a><div class="flip-entry-title">Áudio</div></div>
<div class="flip-entry" id="entry-ABCDEFGHIJ3"><a href="https://docs.google.com/document/d/ABCDEFGHIJ3/edit"><img src="https://drive-thirdparty.googleusercontent.com/16/type/application/vnd.google-apps.document"></a><div class="flip-entry-title">Roteiro</div></div>
<div class="flip-entry" id="entry-ABCDEFGHIJ4"><img src="https://drive-thirdparty.googleusercontent.com/16/type/application/vnd.google-apps.form"><div class="flip-entry-title">Form</div></div>`
	ns := parseEmbedded(page)
	if len(ns) != 3 {
		t.Fatalf("%d nós: %+v", len(ns), ns)
	}
	if ns[0].Name != "Take 01 & 02.mov" || ns[0].Kind != "file" || ns[1].Kind != "folder" || ns[2].Kind != "doc" || ns[2].Name != "Roteiro.docx" {
		t.Errorf("%+v %+v %+v", ns[0], ns[1], ns[2])
	}
}

// ---------- listagem ----------

func buildTree(e *env) {
	mk := e.mock
	mk.addFile("file-aaaaaaa1", "a.mov", randBytes(1000, 1))
	mk.addFile("file-aaaaaaa2", "b.mov", randBytes(2000, 2))
	mk.addFile("file-aaaaaaa3", "Fotos & Vídeos.mp4", randBytes(3000, 3))
	mk.addFolder("fold-sub-00001", "Brutos", "file-aaaaaaa2", "file-aaaaaaa3")
	mk.addFolder("fold-root-0001", "Cliente X", "file-aaaaaaa1", "fold-sub-00001")
}

func checkTree(t *testing.T, n *Node) {
	t.Helper()
	if n.Name != "Cliente X" || len(n.Children) != 2 {
		t.Fatalf("raiz: %+v", n)
	}
	var sub *Node
	for _, c := range n.Children {
		if c.Kind == "folder" {
			sub = c
		}
	}
	if sub == nil || sub.Name != "Brutos" || len(sub.Children) != 2 {
		t.Fatalf("sub: %+v", sub)
	}
	found := false
	for _, c := range sub.Children {
		if c.Name == "Fotos & Vídeos.mp4" {
			found = true
		}
	}
	if !found {
		t.Errorf("nome com & e acento perdido: %+v %+v", sub.Children[0], sub.Children[1])
	}
}

func TestBuildFolderKeyless(t *testing.T) {
	e := newEnv(t, nil)
	buildTree(e)
	l := Link{Kind: KindFolder, ID: "fold-root-0001"}
	n, err := e.f.BuildFolder(context.Background(), l, func(int, string) {})
	if err != nil {
		t.Fatal(err)
	}
	checkTree(t, n)

	e.mock.embeddedEmpty = true // forca o caminho alternativo (_DRIVE_ivd)
	n, err = e.f.BuildFolder(context.Background(), l, func(int, string) {})
	if err != nil {
		t.Fatal(err)
	}
	checkTree(t, n)
}

func TestFolderNotFound(t *testing.T) {
	e := newEnv(t, nil)
	_, err := e.f.BuildFolder(context.Background(), Link{Kind: KindFolder, ID: "nao-existe-123"}, func(int, string) {})
	if err == nil || asDriveError(err).Code != ErrNotFound {
		t.Fatalf("esperava notfound, veio %v", err)
	}
}

// ---------- downloads (modo sem chave) ----------

func TestDownloadParallel(t *testing.T) {
	e := newEnv(t, nil)
	data := randBytes(24<<20, 7)
	e.mock.addFile("file-parallel1", "grande.mov", data)
	e.mock.bps.Store(6 << 20)
	j := e.job("grande", fileItem("file-parallel1", "grande.mov", -1))
	e.mustDone(j)
	if !bytes.Equal(e.read("grande.mov"), data) {
		t.Fatal("conteúdo diferente")
	}
	if p := e.mock.peak.Load(); p < 4 {
		t.Errorf("pico de conexões simultâneas = %d; esperava paralelismo >= 4", p)
	}
	for _, s := range []string{".drivecrtv", ".drivecrtv.json"} {
		if _, err := os.Stat(filepath.Join(e.dest, "grande.mov"+s)); err == nil {
			t.Errorf("sobrou arquivo temporário %s", s)
		}
	}
}

func TestInterstitialRedirectExpiry(t *testing.T) {
	e := newEnv(t, func(s *Settings) { s.ConnsPerFile = 4 })
	data := randBytes(16<<20, 9)
	f := e.mock.addFile("file-interst01", "pesado.mov", data)
	f.interstitial, f.redirect = true, true
	e.mock.bps.Store(4 << 20)
	j := e.job("pesado", fileItem("file-interst01", "pesado.mov", -1))
	go func() {
		time.Sleep(300 * time.Millisecond)
		e.mock.sigValid.Add(1) // expira o endereco assinado no meio do download
	}()
	e.mustDone(j)
	if !bytes.Equal(e.read("pesado.mov"), data) {
		t.Fatal("conteúdo diferente")
	}
	if n := e.mock.probes.Load(); n < 2 {
		t.Errorf("o endereço não foi renovado (probes=%d)", n)
	}
}

func TestQuotaAndPrivate(t *testing.T) {
	e := newEnv(t, nil)
	e.mock.addFile("file-quota00001", "q.mov", randBytes(5000, 1)).quota = true
	e.mock.addFile("file-private001", "p.mov", randBytes(5000, 2)).private = true
	e.mock.addFile("file-okokokok01", "ok.mov", randBytes(5000, 3))
	j := e.job("mix", fileItem("file-quota00001", "q.mov", -1), fileItem("file-private001", "p.mov", -1), fileItem("file-okokokok01", "ok.mov", -1))
	v := e.wait(j, 20*time.Second)
	if v.State != "failed" || v.FilesFailed != 2 || v.FilesDone != 1 {
		t.Fatalf("%+v", v)
	}
	codes := map[string]string{}
	for _, iv := range j.itemViews() {
		codes[iv.Name] = iv.ErrCode
	}
	if codes["q.mov"] != ErrQuota || codes["p.mov"] != ErrPrivate {
		t.Errorf("códigos: %v", codes)
	}
}

func TestFlakyServer(t *testing.T) {
	e := newEnv(t, func(s *Settings) { s.ConnsPerFile = 4 })
	data := randBytes(12<<20, 11)
	e.mock.addFile("file-flaky00001", "flaky.mov", data)
	e.mock.failLeft = 6
	e.mock.cutAfter, e.mock.cutsLeft = 100<<10, 4
	j := e.job("flaky", fileItem("file-flaky00001", "flaky.mov", -1))
	e.mustDone(j)
	if !bytes.Equal(e.read("flaky.mov"), data) {
		t.Fatal("conteúdo diferente após falhas")
	}
}

func TestPauseResume(t *testing.T) {
	e := newEnv(t, func(s *Settings) { s.ConnsPerFile = 4 })
	data := randBytes(40<<20, 13)
	e.mock.addFile("file-resume0001", "big.bin", data)
	e.mock.bps.Store(2 << 20)
	j := e.job("big", fileItem("file-resume0001", "big.bin", -1))
	it := j.items[0]
	for i := 0; i < 600 && it.done.Load() < 22<<20; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if it.done.Load() < 22<<20 {
		t.Fatal("download não andou")
	}
	e.m.Pause(j.ID)
	e.wait(j, 10*time.Second, "paused")
	for _, s := range []string{".drivecrtv", ".drivecrtv.json"} {
		if _, err := os.Stat(filepath.Join(e.dest, "big.bin"+s)); err != nil {
			t.Fatalf("faltou %s com o job pausado", s)
		}
	}
	e.mock.bps.Store(0)
	before := e.mock.served.Load()
	e.m.Resume(j.ID)
	e.mustDone(j)
	if !bytes.Equal(e.read("big.bin"), data) {
		t.Fatal("conteúdo diferente após retomar")
	}
	if again := e.mock.served.Load() - before; again > 40<<20-12<<20 {
		t.Errorf("retomada baixou de novo %d MB; esperava bem menos que o arquivo todo", again>>20)
	}
}

func TestNoRangeSupport(t *testing.T) {
	e := newEnv(t, nil)
	data := randBytes(6<<20, 15)
	e.mock.addFile("file-norange001", "stream.mov", data).noRange = true
	j := e.job("stream", fileItem("file-norange001", "stream.mov", -1))
	e.mustDone(j)
	if !bytes.Equal(e.read("stream.mov"), data) {
		t.Fatal("conteúdo diferente")
	}
}

func TestDocExport(t *testing.T) {
	e := newEnv(t, nil)
	data := []byte("PK-fake-docx-bytes")
	e.mock.addFile("doc-aaaaaaaaaa1", "Roteiro.docx", data)
	j := e.job("docs", AddItem{ID: "doc-aaaaaaaaaa1", Name: "Roteiro.docx", Path: "Roteiro.docx", Kind: "doc", Size: -1})
	e.mustDone(j)
	if !bytes.Equal(e.read("Roteiro.docx"), data) {
		t.Fatal("conteúdo diferente")
	}
}

func TestExistingFiles(t *testing.T) {
	e := newEnv(t, nil)
	same, diff := randBytes(1000, 21), randBytes(2000, 22)
	e.mock.addFile("file-same0000001", "same.bin", same)
	e.mock.addFile("file-diff0000001", "diff.bin", diff)
	_ = os.WriteFile(filepath.Join(e.dest, "same.bin"), same, 0o644)
	_ = os.WriteFile(filepath.Join(e.dest, "diff.bin"), []byte("outro conteudo"), 0o644)
	j := e.job("exist", fileItem("file-same0000001", "same.bin", -1), fileItem("file-diff0000001", "diff.bin", -1))
	e.mustDone(j)
	if string(e.read("diff.bin")) != "outro conteudo" {
		t.Error("sobrescreveu um arquivo existente diferente")
	}
	if !bytes.Equal(e.read("diff (1).bin"), diff) {
		t.Error("não salvou a cópia como 'diff (1).bin'")
	}
	states := map[string]string{}
	for _, iv := range j.itemViews() {
		states[iv.Name] = iv.State
	}
	if states["same.bin"] != "skipped" {
		t.Errorf("estados: %v", states)
	}
}

func TestPersistence(t *testing.T) {
	e := newEnv(t, func(s *Settings) { s.ConnsPerFile = 4 })
	data := randBytes(20<<20, 31)
	e.mock.addFile("file-persist001", "p.bin", data)
	e.mock.bps.Store(2 << 20)
	j := e.job("persist", fileItem("file-persist001", "p.bin", -1))
	for i := 0; i < 300 && j.items[0].done.Load() < 4<<20; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	e.m.Pause(j.ID)
	e.wait(j, 10*time.Second, "paused")
	e.m.saveJobs()

	m2 := newManager(e.st, e.f, e.dd)
	m2.loadJobs()
	snap := m2.Snapshot()
	if len(snap) != 1 || snap[0].State != "paused" {
		t.Fatalf("snapshot restaurado: %+v", snap)
	}
	go m2.dispatchLoop()
	defer m2.Shutdown()
	e.mock.bps.Store(0)
	m2.Resume(snap[0].ID)
	j2 := m2.findJob(snap[0].ID)
	e.wait(j2, 30*time.Second)
	if !bytes.Equal(e.read("p.bin"), data) {
		t.Fatal("conteúdo diferente após restaurar a sessão")
	}
}

// ---------- API com chave ----------

func TestAPIMode(t *testing.T) {
	e := newEnv(t, func(s *Settings) { s.APIKey = "KEY123" })
	e.mock.apiKey, e.mock.apiPage = "KEY123", 2
	var kids []string
	datas := map[string][]byte{}
	for i := 0; i < 5; i++ {
		id := "api-file-" + string(rune('a'+i)) + "000001"
		d := randBytes(300_000+i, int64(40+i))
		e.mock.addFile(id, "clip"+string(rune('a'+i))+".mov", d)
		datas[id] = d
		kids = append(kids, id)
	}
	e.mock.addFolder("api-folder-0001", "Pasta API", kids...)
	n, err := e.f.BuildFolder(context.Background(), Link{Kind: KindFolder, ID: "api-folder-0001"}, func(int, string) {})
	if err != nil {
		t.Fatal(err)
	}
	if n.Name != "Pasta API" || len(n.Children) != 5 || n.Children[0].Size < 300_000 || len(n.Children[0].MD5) != 32 {
		t.Fatalf("listagem via API: %+v", n)
	}
	var items []AddItem
	for _, c := range n.Children {
		items = append(items, AddItem{ID: c.ID, Name: c.Name, Path: c.Name, Kind: c.Kind, Size: c.Size, MD5: c.MD5})
	}
	j := e.job("api", items...)
	e.mustDone(j)
	for _, c := range n.Children {
		if !bytes.Equal(e.read(c.Name), datas[c.ID]) {
			t.Errorf("%s diferente", c.Name)
		}
	}
	if e.mock.probes.Load() != 0 {
		t.Error("usou o modo sem chave apesar de ter chave válida")
	}
}

func TestBadKeyFallsBack(t *testing.T) {
	e := newEnv(t, func(s *Settings) { s.APIKey = "ERRADA" })
	e.mock.apiKey = "CERTA"
	buildTree(e)
	n, err := e.f.BuildFolder(context.Background(), Link{Kind: KindFolder, ID: "fold-root-0001"}, func(int, string) {})
	if err != nil {
		t.Fatal(err)
	}
	checkTree(t, n)
	if !e.f.apiBad.Load() {
		t.Error("deveria marcar a chave como recusada")
	}
	// e o download tambem funciona sem chave
	j := e.job("fb", fileItem("file-aaaaaaa1", "a.mov", -1))
	e.mustDone(j)
}

func TestMD5Mismatch(t *testing.T) {
	e := newEnv(t, func(s *Settings) { s.APIKey = "K" })
	e.mock.apiKey = "K"
	f := e.mock.addFile("api-badmd5-0001", "x.bin", randBytes(200_000, 5))
	f.md5Override = "00000000000000000000000000000000"
	j := e.job("md5", AddItem{ID: "api-badmd5-0001", Name: "x.bin", Path: "x.bin", Kind: "file", Size: 200_000, MD5: f.md5Override})
	v := e.wait(j, 30*time.Second)
	if v.State != "failed" {
		t.Fatalf("esperava falha por MD5, veio %+v", v)
	}
	if iv := j.itemViews()[0]; iv.ErrCode != ErrIntegrity {
		t.Errorf("código = %q", iv.ErrCode)
	}
	if _, err := os.Stat(filepath.Join(e.dest, "x.bin")); err == nil {
		t.Error("não devia ter deixado um arquivo corrompido no destino")
	}
}
