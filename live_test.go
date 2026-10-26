package main

import (
	"context"
	"net/url"
	"os"
	"testing"
	"time"
)

// TestLive roda contra o Google de verdade. So executa com DRIVECRTV_LIVE_FOLDER=<id de pasta publica>.
// Lista a pasta, resolve cada arquivo e baixa por alguns segundos medindo a velocidade.
func TestLive(t *testing.T) {
	id := os.Getenv("DRIVECRTV_LIVE_FOLDER")
	if id == "" {
		t.Skip("defina DRIVECRTV_LIVE_FOLDER para testar contra o Google real")
	}
	dd := t.TempDir()
	st := newStore(dd)
	f := newFetcher(st, defaultEndpoints())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	n, err := f.BuildFolder(ctx, Link{Kind: KindFolder, ID: id}, func(int, string) {})
	if err != nil {
		t.Fatalf("listagem: %v", err)
	}
	t.Logf("pasta %q: %d itens, truncated=%v", n.Name, len(n.Children), n.Truncated)
	var pick *Node
	var pickSize int64
	for _, c := range n.Children {
		if c.Kind != "file" {
			t.Logf("  [%s] %s", c.Kind, c.Name)
			continue
		}
		res, err := f.Resolve(ctx, ItemRef{ID: c.ID, Kind: c.Kind, Size: -1})
		if err != nil {
			t.Logf("  %s -> ERRO %v", c.Name, err)
			continue
		}
		u, _ := url.Parse(res.URL)
		t.Logf("  %s -> nome=%q tamanho=%s ranges=%v host=%s", c.Name, res.Name, humanBytes(res.Size), res.Ranges, u.Host)
		if res.Ranges && res.Size > 0 && (pick == nil || res.Size < pickSize) {
			pick, pickSize = c, res.Size
		}
	}
	if pick == nil {
		t.Fatal("nenhum arquivo com suporte a Range para testar o download")
	}

	st.Update(func(s *Settings) { s.ConnsPerFile, s.MaxConns, s.ChunkMB, s.VerifyMD5 = 8, 16, 16, false })
	m := newManager(st, f, dd)
	go m.dispatchLoop()
	dest := t.TempDir()
	j, err := m.AddJob(AddReq{Title: "live", Dest: dest, Items: []AddItem{{ID: pick.ID, Name: pick.Name, Path: pick.Name, Kind: "file", Size: -1}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("baixando %q (%s) por até 15s...", pick.Name, humanBytes(pickSize))
	start := time.Now()
	var lastDone int64
	for time.Since(start) < 15*time.Second {
		time.Sleep(time.Second)
		it := j.items[0]
		d := it.done.Load()
		t.Logf("  t=%2ds  %s (%s/s) estado=%s", int(time.Since(start).Seconds()), humanBytes(d), humanBytes(d-lastDone), it.getState())
		lastDone = d
		if v := j.view(); v.State == "done" || v.State == "failed" {
			break
		}
	}
	v := j.view()
	t.Logf("fim: estado=%s feito=%s de %s em %.1fs (média %s/s) err=%q", v.State, humanBytes(v.Done), humanBytes(v.Total),
		time.Since(start).Seconds(), humanBytes(int64(float64(m.total.Load())/time.Since(start).Seconds())), v.Err)
	if v.State == "failed" {
		t.Fatalf("falhou: %s", v.Err)
	}
	m.Cancel(j.ID)
	time.Sleep(500 * time.Millisecond)
	m.Shutdown()
}
