package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"
)

// staticHandler serves the embedded console. Unknown paths that do not look
// like asset requests fall back to index.html so client-side routes work.
type staticHandler struct {
	files     fs.FS
	index     []byte
	hasIndex  bool
	modTime   time.Time
	fileServe http.Handler
	csp       string
}

func newStaticHandler(files fs.FS) *staticHandler {
	h := &staticHandler{files: files, modTime: time.Now(), fileServe: http.FileServer(http.FS(files))}
	if f, err := files.Open("index.html"); err == nil {
		defer func() { _ = f.Close() }()
		if b, err := io.ReadAll(f); err == nil {
			h.index = b
			h.hasIndex = true
		}
	}
	h.csp = contentSecurityPolicy(inlineScriptHashes(h.index))
	return h
}

// inlineScriptRe matches classic inline scripts (no src attribute) such as the
// theme bootstrap snippet that must run before first paint.
var inlineScriptRe = regexp.MustCompile(`(?is)<script(\s[^>]*)?>(.*?)</script>`)

// inlineScriptHashes returns CSP source expressions for the inline scripts of index.html.
func inlineScriptHashes(index []byte) []string {
	var out []string
	for _, m := range inlineScriptRe.FindAllSubmatch(index, -1) {
		attrs := strings.ToLower(string(m[1]))
		if strings.Contains(attrs, "src=") || len(bytes.TrimSpace(m[2])) == 0 {
			continue
		}
		sum := sha256.Sum256(m[2])
		out = append(out, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
	}
	return out
}

func contentSecurityPolicy(scriptHashes []string) string {
	scriptSrc := "'self'"
	if len(scriptHashes) > 0 {
		scriptSrc += " " + strings.Join(scriptHashes, " ")
	}
	return "default-src 'self'; script-src " + scriptSrc + "; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; " +
		"font-src 'self' data:; connect-src 'self'; worker-src 'self' blob:; frame-ancestors 'none'; base-uri 'self'; form-action 'self'"
}

func (h *staticHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	setSecurityHeaders(w.Header(), h.csp)
	if !h.hasIndex {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, "Spinneret console is not built into this binary (run `pnpm build` in web/ before `go build`).\n")
		return
	}
	clean := path.Clean("/" + r.URL.Path)
	name := strings.TrimPrefix(clean, "/")
	if name != "" && name != "index.html" {
		if info, err := fs.Stat(h.files, name); err == nil && !info.IsDir() {
			if strings.HasPrefix(name, "assets/") {
				// Vite emits content-hashed file names under assets/.
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "public, max-age=300")
			}
			h.fileServe.ServeHTTP(w, r)
			return
		}
		if path.Ext(name) != "" {
			http.NotFound(w, r)
			return
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "index.html", h.modTime, bytes.NewReader(h.index))
}

// setSecurityHeaders applies browser hardening headers for the console.
func setSecurityHeaders(h http.Header, csp string) {
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("Content-Security-Policy", csp)
}
