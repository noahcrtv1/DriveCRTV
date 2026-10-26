package main

import (
	"net/url"
	"regexp"
	"strings"
)

// LinkKind identifica o tipo de item que um link do Drive aponta.
type LinkKind string

const (
	KindFile   LinkKind = "file"
	KindFolder LinkKind = "folder"
	KindDoc    LinkKind = "doc"
	KindSheet  LinkKind = "sheet"
	KindSlide  LinkKind = "slide"
	KindDraw   LinkKind = "draw"
)

// Link e um item do Drive extraido de um texto colado.
type Link struct {
	Kind        LinkKind `json:"kind"`
	ID          string   `json:"id"`
	ResourceKey string   `json:"resourceKey,omitempty"`
	Source      string   `json:"source"`
}

const idPattern = `[A-Za-z0-9_-]{10,}`

var (
	urlRe     = regexp.MustCompile(`https?://[^\s"'<>\)\]]+`)
	reFolders = regexp.MustCompile(`/folders/(` + idPattern + `)`)
	reFileD   = regexp.MustCompile(`/file/d/(` + idPattern + `)`)
	reDocD    = regexp.MustCompile(`/(document|spreadsheets|presentation|drawings|forms)/d/(` + idPattern + `)`)
	reIDOnly  = regexp.MustCompile(`^` + idPattern + `$`)
)

// parseLinks encontra todos os links do Drive/Docs num texto (um por linha ou misturados).
func parseLinks(text string) []Link {
	var out []Link
	seen := map[string]bool{}
	for _, raw := range urlRe.FindAllString(text, -1) {
		raw = strings.TrimRight(raw, ".,;:!")
		l, ok := parseLink(raw)
		if !ok {
			continue
		}
		k := string(l.Kind) + ":" + l.ID
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, l)
	}
	return out
}

func parseLink(raw string) (Link, bool) {
	u, err := url.Parse(raw)
	if err != nil {
		return Link{}, false
	}
	host := strings.ToLower(u.Hostname())
	switch host {
	case "drive.google.com", "docs.google.com", "drive.usercontent.google.com":
	default:
		return Link{}, false
	}
	q := u.Query()
	l := Link{Source: raw, ResourceKey: q.Get("resourcekey")}
	if l.ResourceKey == "" {
		l.ResourceKey = q.Get("resourceKey")
	}
	p := u.Path

	if m := reFolders.FindStringSubmatch(p); m != nil {
		l.Kind, l.ID = KindFolder, m[1]
		return l, true
	}
	if m := reFileD.FindStringSubmatch(p); m != nil {
		l.Kind, l.ID = KindFile, m[1]
		return l, true
	}
	if m := reDocD.FindStringSubmatch(p); m != nil {
		switch m[1] {
		case "document":
			l.Kind = KindDoc
		case "spreadsheets":
			l.Kind = KindSheet
		case "presentation":
			l.Kind = KindSlide
		case "drawings":
			l.Kind = KindDraw
		default:
			return Link{}, false // formularios nao podem ser exportados
		}
		l.ID = m[2]
		return l, true
	}
	id := q.Get("id")
	if reIDOnly.MatchString(id) {
		switch {
		case strings.HasSuffix(p, "/folderview"):
			l.Kind = KindFolder
		case p == "/open" || p == "/uc" || p == "/download":
			l.Kind = KindFile
		default:
			return Link{}, false
		}
		l.ID = id
		return l, true
	}
	return Link{}, false
}

// kindFromMime converte o mimeType do Drive no nosso tipo de nó.
func kindFromMime(mime string) (LinkKind, bool) {
	switch mime {
	case "application/vnd.google-apps.folder":
		return KindFolder, true
	case "application/vnd.google-apps.document":
		return KindDoc, true
	case "application/vnd.google-apps.spreadsheet":
		return KindSheet, true
	case "application/vnd.google-apps.presentation":
		return KindSlide, true
	case "application/vnd.google-apps.drawing":
		return KindDraw, true
	case "application/vnd.google-apps.form", "application/vnd.google-apps.site",
		"application/vnd.google-apps.map", "application/vnd.google-apps.script",
		"application/vnd.google-apps.jam", "application/vnd.google-apps.fusiontable":
		return "", false // sem exportacao util
	}
	return KindFile, true
}

// exportExt devolve a extensao usada ao exportar arquivos nativos do Google.
func exportExt(k LinkKind) string {
	switch k {
	case KindDoc:
		return ".docx"
	case KindSheet:
		return ".xlsx"
	case KindSlide:
		return ".pptx"
	case KindDraw:
		return ".png"
	}
	return ""
}
