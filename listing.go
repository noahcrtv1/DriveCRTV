package main

import (
	"context"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"
)

// Node e um arquivo ou pasta na arvore mostrada ao usuario.
type Node struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Kind        string  `json:"kind"`
	Mime        string  `json:"mime,omitempty"`
	Size        int64   `json:"size"`
	MD5         string  `json:"md5,omitempty"`
	ResourceKey string  `json:"resourceKey,omitempty"`
	Children    []*Node `json:"children,omitempty"`
	Truncated   bool    `json:"truncated,omitempty"`
	Warn        string  `json:"warn,omitempty"`
}

const (
	maxScanNodes = 50000
	maxScanDepth = 24
	// No modo sem chave o Google costuma entregar no maximo 50 itens por pasta.
	keylessPageHint = 50
)

// ---------- modo sem chave ----------

func (f *Fetcher) fetchPage(ctx context.Context, u string) (string, *url.URL, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", nil, 0, derr(ErrOther, "Link inválido.", false)
	}
	setCommon(req)
	resp, err := f.client().Do(req)
	if err != nil {
		return "", nil, 0, asDriveError(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if strings.HasSuffix(resp.Request.URL.Hostname(), "accounts.google.com") {
		return "", resp.Request.URL, resp.StatusCode, errPrivate()
	}
	return string(b), resp.Request.URL, resp.StatusCode, nil
}

var (
	entryRe   = regexp.MustCompile(`id=["']entry-([A-Za-z0-9_-]{10,})["']`)
	titleRe   = regexp.MustCompile(`(?is)class=["']flip-entry-title["'][^>]*>(.*?)</div>`)
	iconRe    = regexp.MustCompile(`/type/([A-Za-z0-9.+_/-]+)`)
	tagRe     = regexp.MustCompile(`(?s)<[^>]*>`)
	ivdRe     = regexp.MustCompile(`window\['_DRIVE_ivd'\]\s*=\s*'((?:[^'\\]|\\.)*)'`)
	pageTitle = regexp.MustCompile(`(?is)<title>(.*?)</title>`)
	titleTail = regexp.MustCompile(`\s*[-–—]\s*Google Drive\s*$`)
)

func nodeFromMime(id, name, mime string) *Node {
	k, ok := kindFromMime(mime)
	if !ok {
		return nil
	}
	n := &Node{ID: id, Name: name, Kind: string(k), Mime: mime, Size: -1}
	if ext := exportExt(k); ext != "" && !strings.HasSuffix(strings.ToLower(name), ext) {
		n.Name = name + ext
	}
	return n
}

// parseEmbedded le a visao embutida de pasta (drive.google.com/embeddedfolderview).
func parseEmbedded(page string) []*Node {
	idx := entryRe.FindAllStringSubmatchIndex(page, -1)
	var out []*Node
	for i, m := range idx {
		end := len(page)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		seg := page[m[0]:end]
		id := page[m[2]:m[3]]
		name := ""
		if t := titleRe.FindStringSubmatch(seg); t != nil {
			name = strings.TrimSpace(html.UnescapeString(tagRe.ReplaceAllString(t[1], "")))
		}
		if name == "" {
			continue
		}
		mime := ""
		if ic := iconRe.FindStringSubmatch(seg); ic != nil {
			mime = ic[1]
		}
		if strings.Contains(seg, "/folders/") || strings.Contains(seg, "embeddedfolderview") {
			mime = "application/vnd.google-apps.folder"
		}
		if n := nodeFromMime(id, name, mime); n != nil {
			out = append(out, n)
		}
	}
	return out
}

func unescapeJS(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 >= len(s) {
			b.WriteByte(c)
			continue
		}
		i++
		switch s[i] {
		case 'x':
			if i+2 < len(s) || i+2 == len(s)-0 {
				if i+2 <= len(s)-1 || i+2 == len(s) {
					if v, err := strconv.ParseUint(s[i+1:min(i+3, len(s))], 16, 32); err == nil {
						b.WriteRune(rune(v))
						i += 2
						continue
					}
				}
			}
			b.WriteByte('x')
		case 'u':
			if i+4 < len(s)+0 && i+5 <= len(s) {
				if v, err := strconv.ParseUint(s[i+1:i+5], 16, 32); err == nil {
					b.WriteRune(rune(v))
					i += 4
					continue
				}
			}
			b.WriteByte('u')
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		default:
			b.WriteByte(s[i])
		}
	}
	out := b.String()
	if !utf8.ValidString(out) {
		out = strings.ToValidUTF8(out, "")
	}
	return out
}

// parseIVD le o JSON embutido na pagina normal da pasta (formato usado pelo gdown).
func parseIVD(page string) []*Node {
	m := ivdRe.FindStringSubmatch(page)
	if m == nil {
		return nil
	}
	var arr []any
	if json.Unmarshal([]byte(unescapeJS(m[1])), &arr) != nil || len(arr) == 0 {
		return nil
	}
	list, _ := arr[0].([]any)
	var out []*Node
	for _, it := range list {
		row, _ := it.([]any)
		if len(row) < 4 {
			continue
		}
		id, _ := row[0].(string)
		name, _ := row[2].(string)
		mime, _ := row[3].(string)
		if id == "" || name == "" {
			continue
		}
		if n := nodeFromMime(id, name, mime); n != nil {
			out = append(out, n)
		}
	}
	return out
}

func (f *Fetcher) listKeyless(ctx context.Context, id, rk string) ([]*Node, bool, error) {
	q := url.Values{"id": {id}, "hl": {"en"}}
	if rk != "" {
		q.Set("resourcekey", rk)
	}
	body, _, status, err := f.fetchPage(ctx, f.ep.Drive+"/embeddedfolderview?"+q.Encode())
	if err != nil {
		return nil, false, err
	}
	nodes := parseEmbedded(body)
	lower := strings.ToLower(body)
	if len(nodes) == 0 {
		q2 := url.Values{"hl": {"en"}}
		if rk != "" {
			q2.Set("resourcekey", rk)
		}
		b2, _, st2, err2 := f.fetchPage(ctx, f.ep.Drive+"/drive/folders/"+id+"?"+q2.Encode())
		if err2 != nil {
			return nil, false, err2
		}
		nodes = parseIVD(b2)
		lower += strings.ToLower(b2)
		if status == 404 && st2 == 404 {
			return nil, false, errNotFound()
		}
	}
	if len(nodes) == 0 {
		if containsAny(lower, privateMarkers) || status == 401 || status == 403 {
			return nil, false, errPrivate()
		}
		if status == 404 || containsAny(lower, notFoundMarkers) {
			return nil, false, errNotFound()
		}
		return nil, false, derr(ErrHTML, "Não consegui ler o conteúdo desta pasta. Ela pode estar vazia, não ser pública, ou o Google mudou o formato. Configure uma chave de API nos Ajustes para uma listagem completa.", false)
	}
	return nodes, len(nodes) >= keylessPageHint, nil
}

// ---------- API oficial ----------

func nodeFromAPI(af apiFile) *Node {
	id, mime, rk := af.ID, af.MimeType, af.ResourceKey
	if mime == "application/vnd.google-apps.shortcut" && af.ShortcutDetails != nil {
		id, mime, rk = af.ShortcutDetails.TargetID, af.ShortcutDetails.TargetMimeType, af.ShortcutDetails.TargetResourceKey
	}
	n := nodeFromMime(id, af.Name, mime)
	if n == nil {
		return nil
	}
	n.ResourceKey = rk
	n.MD5 = af.MD5
	if sz, err := strconv.ParseInt(af.Size, 10, 64); err == nil {
		n.Size = sz
	}
	return n
}

func (f *Fetcher) listAPI(ctx context.Context, id, rk string) ([]*Node, error) {
	var out []*Node
	token := ""
	for {
		q := url.Values{}
		q.Set("q", "'"+id+"' in parents and trashed = false")
		q.Set("pageSize", "1000")
		q.Set("fields", "nextPageToken,files(id,name,mimeType,size,md5Checksum,resourceKey,shortcutDetails)")
		q.Set("supportsAllDrives", "true")
		q.Set("includeItemsFromAllDrives", "true")
		q.Set("orderBy", "folder,name")
		if token != "" {
			q.Set("pageToken", token)
		}
		var page struct {
			NextPageToken string    `json:"nextPageToken"`
			Files         []apiFile `json:"files"`
		}
		if err := f.apiGetJSON(ctx, f.ep.API+"/drive/v3/files", q, resKeyHeader(id, rk), &page); err != nil {
			return nil, err
		}
		for _, af := range page.Files {
			if n := nodeFromAPI(af); n != nil {
				out = append(out, n)
			}
		}
		if page.NextPageToken == "" {
			return out, nil
		}
		token = page.NextPageToken
	}
}

// ---------- arvore ----------

// ListFolder lista os filhos diretos de uma pasta. O bool indica listagem possivelmente incompleta.
func (f *Fetcher) ListFolder(ctx context.Context, id, rk string) ([]*Node, bool, error) {
	if f.apiKey() != "" {
		nodes, err := f.listAPI(ctx, id, rk)
		if err == nil {
			return nodes, false, nil
		}
		if asDriveError(err).Code != ErrBadKey {
			return nil, false, err
		}
	}
	return f.listKeyless(ctx, id, rk)
}

func (f *Fetcher) folderName(ctx context.Context, id, rk string) string {
	if f.apiKey() != "" {
		if m, err := f.apiMeta(ctx, id, rk); err == nil && m.Name != "" {
			return m.Name
		}
	}
	q := url.Values{"hl": {"en"}}
	if rk != "" {
		q.Set("resourcekey", rk)
	}
	body, _, _, err := f.fetchPage(ctx, f.ep.Drive+"/drive/folders/"+id+"?"+q.Encode())
	if err == nil {
		if m := pageTitle.FindStringSubmatch(body); m != nil {
			t := strings.TrimSpace(html.UnescapeString(m[1]))
			t = strings.TrimSpace(titleTail.ReplaceAllString(t, ""))
			if t != "" && !strings.EqualFold(t, "Google Drive") && !strings.Contains(strings.ToLower(t), "sign in") {
				return t
			}
		}
	}
	return "Pasta do Drive"
}

// BuildFolder monta a arvore completa de uma pasta publica, com subpastas em paralelo.
func (f *Fetcher) BuildFolder(ctx context.Context, l Link, prog func(count int, current string)) (*Node, error) {
	root := &Node{ID: l.ID, Kind: string(KindFolder), ResourceKey: l.ResourceKey, Size: -1}
	root.Name = f.folderName(ctx, l.ID, l.ResourceKey)
	var count atomic.Int64
	var visited sync.Map
	visited.Store(l.ID, true)
	sem := make(chan struct{}, 4)

	var fill func(n *Node, depth int, isRoot bool) error
	fill = func(n *Node, depth int, isRoot bool) error {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		kids, trunc, err := f.ListFolder(ctx, n.ID, n.ResourceKey)
		<-sem
		if err != nil {
			if isRoot || ctx.Err() != nil {
				return err
			}
			n.Warn = asDriveError(err).Msg
			return nil
		}
		n.Truncated = trunc
		n.Children = kids
		prog(int(count.Add(int64(len(kids)))), n.Name)
		var wg sync.WaitGroup
		for _, k := range kids {
			if k.Kind != string(KindFolder) {
				continue
			}
			if _, seen := visited.LoadOrStore(k.ID, true); seen {
				k.Warn = "Pasta repetida (atalho em ciclo); ignorada."
				continue
			}
			if depth >= maxScanDepth || count.Load() > maxScanNodes {
				k.Truncated = true
				k.Warn = "Limite de profundidade/itens atingido; esta pasta não foi aberta."
				continue
			}
			wg.Add(1)
			go func(k *Node) {
				defer wg.Done()
				_ = fill(k, depth+1, false)
			}(k)
		}
		wg.Wait()
		return ctx.Err()
	}
	if err := fill(root, 0, true); err != nil {
		return nil, err
	}
	return root, nil
}
