package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html"
	"io"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"
)

const (
	// headPlaceholder in index.html is replaced with <base href> and window.Dupearr.
	headPlaceholder = "<!--DUPEARR_HEAD-->"

	cacheImmutable = "public, max-age=31536000, immutable" // hashed /assets/*
	cacheShort     = "public, max-age=3600"                // favicon, logo, other root files
	cacheNone      = "no-cache"                            // index.html (always revalidate)
)

// staticExts are extensions that name files, never SPA routes: a missing one is a 404 instead of
// the index page (a browser must not parse HTML as a script or stylesheet).
var staticExts = map[string]bool{
	".js": true, ".mjs": true, ".css": true, ".map": true, ".json": true, ".txt": true, ".xml": true,
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".svg": true, ".ico": true, ".webp": true,
	".avif": true, ".woff": true, ".woff2": true, ".ttf": true, ".otf": true, ".webmanifest": true,
	".wasm": true, ".zip": true,
}

// serveSPA serves the embedded web UI: existing files from Deps.WebFS (hashed /assets/* cached
// for a year), and index.html — with the UrlBase injected — for every other path, so client-side
// routes work on reload. The SPA itself redirects to /login when initialize.json answers 401.
func (s *Server) serveSPA(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeMessage(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	clean := path.Clean("/" + r.URL.Path)
	name := strings.TrimPrefix(clean, "/")
	if name != "" && name != "index.html" {
		if s.serveStatic(w, r, name) {
			return
		}
		if clean == "/assets" || strings.HasPrefix(clean, "/assets/") || staticExts[strings.ToLower(path.Ext(name))] {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, "404 page not found\n")
			return
		}
	}
	s.serveIndex(w, r)
}

// serveStatic serves one file from WebFS; it reports false when there is no such file. Hidden
// files and directories are never served.
func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request, name string) bool {
	if s.d.WebFS == nil || !fs.ValidPath(name) {
		return false
	}
	for _, seg := range strings.Split(name, "/") {
		if strings.HasPrefix(seg, ".") {
			return false
		}
	}
	f, err := s.d.WebFS.Open(name)
	if err != nil {
		return false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		return false
	}
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", cacheImmutable)
	} else {
		w.Header().Set("Cache-Control", cacheShort)
	}
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		data, err := io.ReadAll(io.LimitReader(f, 64<<20))
		if err != nil {
			return false
		}
		rs = bytes.NewReader(data)
	}
	http.ServeContent(w, r, path.Base(name), modTime(fi), rs)
	return true
}

func modTime(fi fs.FileInfo) time.Time {
	if t := fi.ModTime(); !t.IsZero() && t.Unix() > 0 {
		return t
	}
	return time.Time{}
}

// serveIndex serves index.html with the UrlBase injected, or a notice when the UI is not built.
// Both carry a Content-Security-Policy (pageCSP).
func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	var page []byte
	if s.d.WebFS != nil {
		data, err := fs.ReadFile(s.d.WebFS, "index.html")
		switch {
		case err == nil:
			page = injectHead(data, s.urlBase())
		case !errors.Is(err, fs.ErrNotExist):
			s.log.Warn("Cannot read the web UI's index.html", "error", err)
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if page == nil {
		w.Header().Set("Content-Security-Policy", pageCSP([]byte(notBuiltPage)))
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, notBuiltPage)
		return
	}
	w.Header().Set("Content-Security-Policy", pageCSP(page))
	w.Header().Set("Cache-Control", cacheNone)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(page)
}

// inlineScriptRe and inlineStyleRe match inline <script> / <style> elements (content in group 2).
var (
	inlineScriptRe = regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script\s*>`)
	inlineStyleRe  = regexp.MustCompile(`(?is)<style\b([^>]*)>(.*?)</style\s*>`)
	srcAttrRe      = regexp.MustCompile(`(?i)\ssrc\s*=`)
)

// pageCSP returns the Content-Security-Policy of an HTML page: only same-origin scripts, styles,
// images (plus data: URIs), fonts and connections; no plugins, no foreign <base>, forms or
// framing. The page's own inline <script> and <style> elements (the theme bootstrap and the
// injected window.Dupearr in index.html) are allowed by their SHA-256 hashes, computed from the
// page actually served, so nothing else inline — an injected <script> or event handler — runs.
// The web UI holds the API key in memory and renders titles and paths from Plex and the *arrs:
// this keeps any future injection from exfiltrating it or driving the API.
func pageCSP(page []byte) string {
	hashes := func(re *regexp.Regexp) string {
		var out []string
		seen := map[string]bool{}
		for _, m := range re.FindAllSubmatch(page, -1) {
			if srcAttrRe.Match(m[1]) {
				continue // <script src=…>: allowed by 'self'
			}
			sum := sha256.Sum256(m[2])
			h := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
			if !seen[h] {
				seen[h] = true
				out = append(out, h)
			}
		}
		if len(out) == 0 {
			return ""
		}
		return " " + strings.Join(out, " ")
	}
	return "default-src 'self'; script-src 'self'" + hashes(inlineScriptRe) +
		"; style-src 'self'" + hashes(inlineStyleRe) +
		"; img-src 'self' data:; font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'self'" +
		"; form-action 'self'; frame-ancestors 'self'"
}

// injectHead replaces the head placeholder with <base href="{base}/"> and
// <script>window.Dupearr={urlBase:"{base}"}</script>. The base is HTML-attribute escaped for the
// href and JSON (JS string, <>& escaped) encoded for the script. Without a placeholder the tags are
// inserted right after <head>.
func injectHead(page []byte, urlBase string) []byte {
	js, err := json.Marshal(urlBase) // escapes <, >, &, U+2028/2029
	if err != nil {
		js = []byte(`""`)
	}
	tags := `<base href="` + html.EscapeString(urlBase+"/") + `"><script>window.Dupearr={urlBase:` + string(js) + `}</script>`
	if bytes.Contains(page, []byte(headPlaceholder)) {
		return bytes.Replace(page, []byte(headPlaceholder), []byte(tags), 1)
	}
	lower := bytes.ToLower(page)
	if i := bytes.Index(lower, []byte("<head>")); i >= 0 {
		i += len("<head>")
		out := make([]byte, 0, len(page)+len(tags))
		out = append(out, page[:i]...)
		out = append(out, tags...)
		return append(out, page[i:]...)
	}
	return append([]byte(tags), page...)
}

// notBuiltPage is served when the binary was built without the web UI.
const notBuiltPage = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Dupearr</title>
<style>body{font-family:system-ui,sans-serif;background:#202020;color:#ccc;display:flex;align-items:center;justify-content:center;min-height:100vh;margin:0}main{max-width:36rem;padding:2rem}h1{color:#8b5cf6}code{background:#333;padding:.1rem .3rem;border-radius:3px}</style>
</head>
<body><main>
<h1>Dupearr</h1>
<p>The web UI has not been built into this binary.</p>
<p>Run <code>make web</code> (or <code>cd web &amp;&amp; npm ci &amp;&amp; npm run build</code>) and rebuild Dupearr. The API is available at <code>/api/v1</code>.</p>
</main></body>
</html>
`
