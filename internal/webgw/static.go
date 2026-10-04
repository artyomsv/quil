package webgw

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// dist holds the built browser client. In a checkout without a web build it
// holds only .keep, and the gateway serves noUIPage instead.
//
//go:embed all:dist
var dist embed.FS

const noUIPage = `<!doctype html><html><head><meta charset="utf-8"><title>Quil</title></head>` +
	`<body><p>This build of quil has no web UI. Install a release build, or build it with ./scripts/dev.sh build.</p></body></html>`

func distFS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // the embed pattern guarantees the directory
	}
	return sub
}

// HasUI reports whether the browser client was built into this binary.
func HasUI() bool {
	_, err := fs.Stat(distFS(), "index.html")
	return err == nil
}

// StaticHandler serves the embedded client, or the no-UI page at "/" when
// the binary was built without it. The files carry no data, so they need no
// session; the security headers are added by the caller's middleware.
func StaticHandler() http.Handler {
	if !HasUI() {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(noUIPage))
		})
	}
	return filesOnly(distFS())
}

// filesOnly serves the files of fsys and answers 404 for every directory but
// the root (which serves index.html), so nothing lists the embedded tree.
func filesOnly(fsys fs.FS) http.Handler {
	files := http.FileServer(http.FS(fsys))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.Trim(path.Clean("/"+r.URL.Path), "/")
		if name != "" {
			if st, err := fs.Stat(fsys, name); err == nil && st.IsDir() {
				http.NotFound(w, r)
				return
			}
		}
		files.ServeHTTP(w, r)
	})
}
