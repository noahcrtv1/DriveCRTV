package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"html"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

// Fetcher concentra o cliente HTTP e a logica de falar com o Google.
type Fetcher struct {
	ep     Endpoints
	st     *Store
	tr     *http.Transport
	apiBad atomic.Bool // chave recusada pelo Google: cai para o modo sem chave
}

// ItemRef descreve um item do Drive que sera resolvido/baixado.
type ItemRef struct {
	ID          string
	Name        string
	Kind        string
	Mime        string
	ResourceKey string
	Size        int64
	MD5         string
}

// Resolved e o endereco direto de download de um item.
type Resolved struct {
	URL     string
	Size    int64 // -1 desconhecido
	Name    string
	Mime    string
	Ranges  bool
	API     bool
	Headers map[string]string
	MD5     string
	ModTime time.Time
}

func newFetcher(st *Store, ep Endpoints) *Fetcher {
	tr := &http.Transport{
		Proxy:       http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		// HTTP/2 multiplexa tudo numa conexao so. Para somar banda queremos varias conexoes TCP.
		TLSNextProto:          map[string]func(string, *tls.Conn) http.RoundTripper{},
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		ExpectContinueTimeout: time.Second,
		DisableCompression:    true,
		ReadBufferSize:        256 << 10,
		WriteBufferSize:       32 << 10,
	}
	return &Fetcher{ep: ep, st: st, tr: tr}
}

func (f *Fetcher) client() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Transport: f.tr, Jar: jar}
}

func setCommon(req *http.Request) {
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Accept", "*/*")
}

// apiKey devolve a chave em uso, ou "" para o modo sem chave.
func (f *Fetcher) apiKey() string {
	if f.apiBad.Load() {
		return ""
	}
	return f.st.Get().APIKey
}

func isHTML(ct string) bool { return strings.Contains(strings.ToLower(ct), "text/html") }

func nameFromHeaders(h http.Header) string {
	cd := h.Get("Content-Disposition")
	if cd == "" {
		return ""
	}
	if _, params, err := mime.ParseMediaType(cd); err == nil && params["filename"] != "" {
		return params["filename"]
	}
	if m := regexp.MustCompile(`filename="([^"]+)"`).FindStringSubmatch(cd); m != nil {
		return m[1]
	}
	return ""
}

func sizeFromContentRange(cr string) int64 {
	if i := strings.LastIndex(cr, "/"); i >= 0 {
		if n, err := strconv.ParseInt(strings.TrimSpace(cr[i+1:]), 10, 64); err == nil {
			return n
		}
	}
	return -1
}

func statusErr(code int) *DriveError {
	switch {
	case code == 401 || code == 403:
		return errPrivate()
	case code == 404:
		return errNotFound()
	case code == 429:
		return derr(ErrRate, "O Google está limitando as requisições. O app tenta de novo em instantes.", true)
	case code >= 500:
		return derr(ErrNet, "O Google respondeu com erro "+strconv.Itoa(code)+" (temporário).", true)
	}
	return derr(ErrOther, "Resposta inesperada do Google (HTTP "+strconv.Itoa(code)+").", false)
}

// ---------- modo sem chave ----------

func (f *Fetcher) startURL(id, rk string) string {
	v := url.Values{}
	v.Set("id", id)
	v.Set("export", "download")
	v.Set("confirm", "t")
	if rk != "" {
		v.Set("resourcekey", rk)
	}
	return f.ep.UserContent + "/download?" + v.Encode()
}

var (
	quotaMarkers = []string{
		"too many users have viewed or downloaded", "download quota", "quota exceeded",
		"muitos usuários visualizaram", "cota de download",
	}
	privateMarkers = []string{
		"you need access", "request access", "you need permission", "servicelogin",
		"precisa de acesso", "solicitar acesso", "precisa de permissão",
	}
	notFoundMarkers = []string{
		"file you have requested does not exist", "can't be found", "could not be found",
		"não foi encontrado", "não existe",
	}
	formTagRe   = regexp.MustCompile(`(?is)<form[^>]*\bid=["'](?:download-form|downloadForm)["'][^>]*>`)
	actionRe    = regexp.MustCompile(`(?i)\baction=["']([^"']+)["']`)
	inputRe     = regexp.MustCompile(`(?is)<input[^>]*>`)
	inNameRe    = regexp.MustCompile(`(?i)\bname=["']([^"']+)["']`)
	inValueRe   = regexp.MustCompile(`(?i)\bvalue=["']([^"']*)["']`)
	hrefConfRe  = regexp.MustCompile(`(?i)href=["'](/uc\?export=download[^"']+)["']`)
	downloadURe = regexp.MustCompile(`"downloadUrl":"([^"]+)"`)
)

func containsAny(s string, markers []string) bool {
	for _, m := range markers {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// interpretHTML entende as paginas HTML do Google (aviso de virus, cota, privado)
// e devolve a proxima URL a tentar.
func (f *Fetcher) interpretHTML(body string, base *url.URL, status int) (string, error) {
	lower := strings.ToLower(body)
	if containsAny(lower, quotaMarkers) {
		return "", errQuota()
	}
	if loc := formTagRe.FindStringIndex(body); loc != nil {
		tag := body[loc[0]:loc[1]]
		action := f.ep.UserContent + "/download"
		if m := actionRe.FindStringSubmatch(tag); m != nil {
			action = html.UnescapeString(m[1])
		}
		rest := body[loc[1]:]
		if end := strings.Index(strings.ToLower(rest), "</form>"); end >= 0 {
			rest = rest[:end]
		}
		v := url.Values{}
		for _, in := range inputRe.FindAllString(rest, -1) {
			nm := inNameRe.FindStringSubmatch(in)
			if nm == nil {
				continue
			}
			val := ""
			if vm := inValueRe.FindStringSubmatch(in); vm != nil {
				val = html.UnescapeString(vm[1])
			}
			v.Set(html.UnescapeString(nm[1]), val)
		}
		if v.Get("confirm") == "" {
			v.Set("confirm", "t")
		}
		au, err := base.Parse(action)
		if err != nil {
			return "", derr(ErrHTML, "Página de confirmação do Google não reconhecida.", false)
		}
		q := au.Query()
		for k, vs := range v {
			q.Set(k, vs[0])
		}
		au.RawQuery = q.Encode()
		return au.String(), nil
	}
	if m := hrefConfRe.FindStringSubmatch(body); m != nil {
		return f.ep.Docs + html.UnescapeString(m[1]), nil
	}
	if m := downloadURe.FindStringSubmatch(body); m != nil {
		u := strings.NewReplacer(`=`, "=", `&`, "&", `\/`, "/").Replace(m[1])
		return u, nil
	}
	if containsAny(lower, privateMarkers) || status == 401 || status == 403 {
		return "", errPrivate()
	}
	if containsAny(lower, notFoundMarkers) || status == 404 {
		return "", errNotFound()
	}
	return "", derr(ErrHTML, "O Google devolveu uma página que o app não entendeu. Tente de novo; se persistir, configure uma chave de API nos Ajustes.", false)
}

func (f *Fetcher) resolveKeyless(ctx context.Context, id, rk string) (*Resolved, error) {
	client := f.client()
	target := f.startURL(id, rk)
	for attempt := 0; attempt < 4; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, derr(ErrOther, "Link inválido.", false)
		}
		setCommon(req)
		req.Header.Set("Range", "bytes=0-0")
		resp, err := client.Do(req)
		if err != nil {
			return nil, asDriveError(err)
		}
		final := resp.Request.URL
		ct := resp.Header.Get("Content-Type")
		if strings.HasSuffix(final.Hostname(), "accounts.google.com") {
			resp.Body.Close()
			return nil, errPrivate()
		}
		mk := func(ranges bool, size int64) *Resolved {
			return &Resolved{
				URL: final.String(), Size: size, Ranges: ranges,
				Name: nameFromHeaders(resp.Header), Mime: strings.TrimSpace(strings.Split(ct, ";")[0]),
			}
		}
		switch {
		case resp.StatusCode == http.StatusPartialContent && !isHTML(ct):
			res := mk(true, sizeFromContentRange(resp.Header.Get("Content-Range")))
			resp.Body.Close()
			return res, nil
		case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
			res := mk(true, 0)
			resp.Body.Close()
			return res, nil
		case resp.StatusCode == http.StatusOK && !isHTML(ct):
			res := mk(false, resp.ContentLength)
			resp.Body.Close()
			return res, nil
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if !isHTML(ct) && len(body) == 0 {
			return nil, statusErr(resp.StatusCode)
		}
		next, derr2 := f.interpretHTML(string(body), final, resp.StatusCode)
		if derr2 != nil {
			return nil, derr2
		}
		target = next
	}
	return nil, derr(ErrHTML, "O Google pediu confirmações demais para este arquivo. Tente de novo mais tarde.", false)
}

// ---------- API oficial (com chave) ----------

type apiFile struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	MimeType        string `json:"mimeType"`
	Size            string `json:"size"`
	MD5             string `json:"md5Checksum"`
	ModifiedTime    string `json:"modifiedTime"`
	ResourceKey     string `json:"resourceKey"`
	ShortcutDetails *struct {
		TargetID          string `json:"targetId"`
		TargetMimeType    string `json:"targetMimeType"`
		TargetResourceKey string `json:"targetResourceKey"`
	} `json:"shortcutDetails"`
}

func mapAPIError(status int, body []byte) *DriveError {
	var ae struct {
		Error struct {
			Message string `json:"message"`
			Errors  []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
			Details []struct {
				Reason string `json:"reason"`
			} `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &ae)
	reason := ""
	if len(ae.Error.Errors) > 0 {
		reason = ae.Error.Errors[0].Reason
	}
	if len(ae.Error.Details) > 0 && ae.Error.Details[0].Reason != "" {
		reason = ae.Error.Details[0].Reason
	}
	msg := ae.Error.Message
	switch reason {
	case "API_KEY_INVALID", "keyInvalid", "accessNotConfigured", "SERVICE_DISABLED",
		"API_KEY_SERVICE_BLOCKED", "API_KEY_HTTP_REFERRER_BLOCKED", "API_KEY_IP_ADDRESS_BLOCKED",
		"API_KEY_ANDROID_APP_BLOCKED", "API_KEY_IOS_APP_BLOCKED", "ipRefererBlocked", "referrerNotAllowed":
		return derr(ErrBadKey, "A chave de API foi recusada pelo Google ("+reason+"). Confira se a Google Drive API está ativada e se a chave não tem restrição de site.", false)
	case "downloadQuotaExceeded":
		return errQuota()
	case "rateLimitExceeded", "userRateLimitExceeded":
		return derr(ErrRate, "Limite de requisições da chave atingido. O app tenta de novo em instantes.", true)
	case "dailyLimitExceeded":
		return derr(ErrRate, "O limite diário da chave de API acabou. Tente amanhã ou use outra chave.", false)
	case "notFound":
		return errNotFound()
	case "cannotDownloadFile", "forbidden", "insufficientFilePermissions", "appNotAuthorizedToFile":
		return errPrivate()
	}
	switch {
	case status == 400 && strings.Contains(strings.ToLower(msg), "api key"):
		return derr(ErrBadKey, "A chave de API é inválida.", false)
	case status == 404:
		return errNotFound()
	case status == 401 || status == 403:
		return errPrivate()
	case status == 429 || status >= 500:
		return statusErr(status)
	}
	if msg == "" {
		msg = "HTTP " + strconv.Itoa(status)
	}
	return derr(ErrOther, "Erro do Google Drive: "+msg, false)
}

func (f *Fetcher) apiGetJSON(ctx context.Context, endpoint string, q url.Values, resKeyHeader string, out any) error {
	key := f.apiKey()
	if key == "" {
		return derr(ErrBadKey, "Sem chave de API.", false)
	}
	q.Set("key", key)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+q.Encode(), nil)
	if err != nil {
		return derr(ErrOther, "Requisição inválida.", false)
	}
	setCommon(req)
	req.Header.Set("Accept", "application/json")
	if resKeyHeader != "" {
		req.Header.Set("X-Goog-Drive-Resource-Keys", resKeyHeader)
	}
	resp, err := f.client().Do(req)
	if err != nil {
		return asDriveError(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode != http.StatusOK {
		e := mapAPIError(resp.StatusCode, body)
		if e.Code == ErrBadKey {
			f.apiBad.Store(true)
		}
		return e
	}
	if err := json.Unmarshal(body, out); err != nil {
		return derr(ErrOther, "Resposta da API ilegível.", false)
	}
	return nil
}

func resKeyHeader(id, rk string) string {
	if rk == "" {
		return ""
	}
	return id + "/" + rk
}

func (f *Fetcher) apiMeta(ctx context.Context, id, rk string) (*apiFile, error) {
	q := url.Values{}
	q.Set("fields", "id,name,mimeType,size,md5Checksum,modifiedTime,resourceKey,shortcutDetails")
	q.Set("supportsAllDrives", "true")
	var out apiFile
	if err := f.apiGetJSON(ctx, f.ep.API+"/drive/v3/files/"+id, q, resKeyHeader(id, rk), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (f *Fetcher) resolveAPI(ctx context.Context, r ItemRef) (*Resolved, error) {
	res := &Resolved{Ranges: true, API: true, Size: r.Size, Name: r.Name, MD5: r.MD5, Mime: r.Mime, Headers: map[string]string{}}
	if h := resKeyHeader(r.ID, r.ResourceKey); h != "" {
		res.Headers["X-Goog-Drive-Resource-Keys"] = h
	}
	if r.Size < 0 || r.Name == "" {
		m, err := f.apiMeta(ctx, r.ID, r.ResourceKey)
		if err != nil {
			return nil, err
		}
		if n, err := strconv.ParseInt(m.Size, 10, 64); err == nil {
			res.Size = n
		}
		res.Name, res.MD5, res.Mime = m.Name, m.MD5, m.MimeType
		if t, err := time.Parse(time.RFC3339, m.ModifiedTime); err == nil {
			res.ModTime = t
		}
	}
	q := url.Values{}
	q.Set("alt", "media")
	q.Set("supportsAllDrives", "true")
	q.Set("key", f.apiKey())
	res.URL = f.ep.API + "/drive/v3/files/" + r.ID + "?" + q.Encode()
	return res, nil
}

// ---------- entrada principal ----------

func (f *Fetcher) exportURL(r ItemRef) string {
	var u string
	switch LinkKind(r.Kind) {
	case KindDoc:
		u = f.ep.Docs + "/document/d/" + r.ID + "/export?format=docx"
	case KindSheet:
		u = f.ep.Docs + "/spreadsheets/d/" + r.ID + "/export?format=xlsx"
	case KindSlide:
		u = f.ep.Docs + "/presentation/d/" + r.ID + "/export/pptx"
	default:
		u = f.ep.Docs + "/drawings/d/" + r.ID + "/export/png"
	}
	if r.ResourceKey != "" {
		sep := "?"
		if strings.Contains(u, "?") {
			sep = "&"
		}
		u += sep + "resourcekey=" + url.QueryEscape(r.ResourceKey)
	}
	return u
}

// Resolve descobre o endereco direto, tamanho e nome de um item.
func (f *Fetcher) Resolve(ctx context.Context, r ItemRef) (*Resolved, error) {
	switch LinkKind(r.Kind) {
	case KindDoc, KindSheet, KindSlide, KindDraw:
		return &Resolved{URL: f.exportURL(r), Size: -1, Name: r.Name}, nil
	}
	if f.apiKey() != "" {
		res, err := f.resolveAPI(ctx, r)
		if err == nil {
			return res, nil
		}
		if asDriveError(err).Code != ErrBadKey {
			return nil, err
		}
	}
	res, err := f.resolveKeyless(ctx, r.ID, r.ResourceKey)
	if err != nil {
		return nil, err
	}
	if res.Name == "" {
		res.Name = r.Name
	}
	return res, nil
}

// probeExport le so os cabecalhos da exportacao de um Doc/Planilha/Slide para descobrir o nome.
func (f *Fetcher) probeExport(ctx context.Context, r ItemRef) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.exportURL(r), nil)
	if err != nil {
		return "", derr(ErrOther, "Link inválido.", false)
	}
	setCommon(req)
	resp, err := f.client().Do(req)
	if err != nil {
		return "", asDriveError(err)
	}
	defer resp.Body.Close()
	if strings.HasSuffix(resp.Request.URL.Hostname(), "accounts.google.com") {
		return "", errPrivate()
	}
	if resp.StatusCode != http.StatusOK || isHTML(resp.Header.Get("Content-Type")) {
		if resp.StatusCode == http.StatusOK {
			return "", errPrivate()
		}
		return "", statusErr(resp.StatusCode)
	}
	return nameFromHeaders(resp.Header), nil
}

// Describe monta o no de um link avulso (arquivo ou documento) colado pelo usuario.
func (f *Fetcher) Describe(ctx context.Context, l Link) (*Node, error) {
	n := &Node{ID: l.ID, Kind: string(l.Kind), ResourceKey: l.ResourceKey, Size: -1}
	switch l.Kind {
	case KindDoc, KindSheet, KindSlide, KindDraw:
		name, err := f.probeExport(ctx, ItemRef{ID: l.ID, Kind: string(l.Kind), ResourceKey: l.ResourceKey})
		if err != nil {
			return nil, err
		}
		if name == "" {
			name = "Documento" + exportExt(l.Kind)
		}
		n.Name = name
		return n, nil
	}
	res, err := f.Resolve(ctx, ItemRef{ID: l.ID, Kind: string(l.Kind), ResourceKey: l.ResourceKey, Size: -1})
	if err != nil {
		return nil, err
	}
	n.Name, n.Size, n.MD5, n.Mime = res.Name, res.Size, res.MD5, res.Mime
	if n.Name == "" {
		n.Name = "arquivo-" + l.ID[:6]
	}
	return n, nil
}
