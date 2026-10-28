// sharefile serves one folder over HTTP so LAN users can list, download and delete files.
package main

import (
	"flag"
	"html/template"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type fileInfo struct {
	Name    string
	Size    int64
	ModTime time.Time
}

var page = template.Must(template.New("").Funcs(template.FuncMap{"pathEscape": url.PathEscape}).Parse(`<!doctype html>
<meta charset="utf-8"><meta name="viewport" content="width=device-width">
<title>Scans</title>
<style>body{font-family:sans-serif;margin:1rem}td,th{padding:.3rem .8rem;text-align:left}</style>
<h1>Scans</h1>
{{if .}}<table>
<tr><th>Name</th><th>Size (bytes)</th><th>Modified</th><th></th></tr>
{{range .}}<tr>
<td><a href="/f/{{pathEscape .Name}}">{{.Name}}</a></td>
<td>{{.Size}}</td>
<td>{{.ModTime.Format "2006-01-02 15:04:05"}}</td>
<td><button data-name="{{.Name}}" onclick="del(this.dataset.name)">Delete</button></td>
</tr>{{end}}
</table>{{else}}<p>(no files)</p>{{end}}
<script>
function del(n) {
  if (!confirm('Delete ' + n + '?')) return;
  fetch('/f/' + encodeURIComponent(n), {method: 'DELETE'})
    .then(r => r.ok ? location.reload() : alert('Delete failed: ' + r.status));
}
</script>`))

func listFiles(root *os.Root) ([]fileInfo, error) {
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return nil, err
	}
	var files []fileInfo
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue // removed between ReadDir and Info
		}
		files = append(files, fileInfo{e.Name(), info.Size(), info.ModTime()})
	}
	slices.SortFunc(files, func(a, b fileInfo) int { return b.ModTime.Compare(a.ModTime) })
	return files, nil
}

// isRegular reports whether name is a regular file (not a dir or symlink) directly inside root.
func isRegular(root *os.Root, name string) bool {
	if strings.ContainsAny(name, `/\`) { // PathValue unescapes %2F and %5C
		return false
	}
	fi, err := root.Lstat(name)
	return err == nil && fi.Mode().IsRegular()
}

func newHandler(root *os.Root) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		files, err := listFiles(root)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		page.Execute(w, files)
	})
	mux.HandleFunc("GET /f/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if !isRegular(root, name) {
			http.NotFound(w, r)
			return
		}
		f, err := root.Open(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		disp := "attachment"
		if r.URL.Query().Get("inline") == "1" && strings.EqualFold(filepath.Ext(name), ".pdf") {
			disp = "inline" // only PDFs render in-browser; anything else always downloads
		}
		w.Header().Set("Content-Disposition", mime.FormatMediaType(disp, map[string]string{"filename": name}))
		// ServeContent, not ServeFileFS: the latter redirects names ending in index.html.
		http.ServeContent(w, r, name, fi.ModTime(), f)
	})
	mux.HandleFunc("DELETE /f/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if !isRegular(root, name) {
			http.NotFound(w, r)
			return
		}
		if err := root.Remove(name); err != nil {
			log.Printf("%s delete %s failed: %v", r.RemoteAddr, name, err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("%s deleted %s", r.RemoteAddr, name)
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

// logOutput opens path for appending. If it can't (e.g. no write access to the
// exe folder), it returns stderr so the server still runs instead of exiting.
func logOutput(path string) io.Writer {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		log.Printf("cannot open log %s: %v", path, err)
		return os.Stderr
	}
	return f
}

func main() {
	dir := flag.String("dir", "", "folder to share (absolute path, required)")
	addr := flag.String("addr", ":8080", "listen address")
	logPath := flag.String("log", "", "log file (default stderr)")
	flag.Parse()

	if *logPath != "" {
		log.SetOutput(logOutput(*logPath))
	}
	if !filepath.IsAbs(*dir) {
		log.Fatalf("-dir must be an absolute path, got %q", *dir)
	}
	root, err := os.OpenRoot(*dir)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("sharing %s on %s", *dir, *addr)
	log.Fatal(http.ListenAndServe(*addr, newHandler(root)))
}
