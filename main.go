// sharefile serves one folder over HTTP so LAN users can list, download and delete files.
package main

import (
	"archive/zip"
	_ "embed"
	"errors"
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
	"strconv"
	"strings"
	"time"
)

type fileInfo struct {
	Name    string
	Size    int64
	ModTime time.Time
}

//go:embed page.html
var pageHTML string

var page = template.Must(template.New("").Funcs(template.FuncMap{
	"pathEscape": url.PathEscape,
	"humanSize":  humanSize,
}).Parse(pageHTML))

type dayGroup struct {
	Label string
	Files []fileInfo
}

var thaiMonths = [...]string{"ม.ค.", "ก.พ.", "มี.ค.", "เม.ย.", "พ.ค.", "มิ.ย.", "ก.ค.", "ส.ค.", "ก.ย.", "ต.ค.", "พ.ย.", "ธ.ค."}

// dayLabel names the calendar day of d relative to now, in Thai
// (Buddhist year only when it differs from now's).
func dayLabel(d, now time.Time) string {
	d = d.In(now.Location())
	y, m, day := d.Date()
	s := strconv.Itoa(day) + " " + thaiMonths[m-1]
	ny, nm, nd := now.Date()
	switch {
	case y == ny && m == nm && day == nd:
		return "วันนี้ · " + s
	case sameDay(d, now.AddDate(0, 0, -1)):
		return "เมื่อวาน · " + s
	case y != ny:
		return s + " " + strconv.Itoa(y+543)
	}
	return s
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

// groupByDay splits newest-first files into one group per local calendar day, keeping order.
func groupByDay(files []fileInfo, now time.Time) []dayGroup {
	var groups []dayGroup
	for _, f := range files {
		label := dayLabel(f.ModTime, now)
		if n := len(groups); n > 0 && groups[n-1].Label == label {
			groups[n-1].Files = append(groups[n-1].Files, f)
		} else {
			groups = append(groups, dayGroup{label, []fileInfo{f}})
		}
	}
	return groups
}

// humanSize formats n as "N B", whole "N KB", or one-decimal "N.N MB".
func humanSize(n int64) string {
	switch {
	case n < 1<<10:
		return strconv.FormatInt(n, 10) + " B"
	case n < 1<<20-512: // below this, rounding to KB stays under 1024
		return strconv.FormatInt((n+512)>>10, 10) + " KB"
	}
	return strconv.FormatFloat(float64(n)/(1<<20), 'f', 1, 64) + " MB"
}

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
		page.Execute(w, groupByDay(files, time.Now()))
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
	mux.HandleFunc("POST /zip", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var names []string
		for _, n := range r.PostForm["name"] {
			if slices.Contains(names, n) {
				continue
			}
			if !isRegular(root, n) {
				log.Printf("%s zip skipped %q: missing or not a regular file", r.RemoteAddr, n)
				continue
			}
			names = append(names, n)
		}
		if len(names) == 0 {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": zipName(time.Now())}))
		zw := zip.NewWriter(w)
		n := 0
		for _, name := range names {
			f, err := root.Open(name)
			if err != nil {
				log.Printf("%s zip skipped %s: %v", r.RemoteAddr, name, err)
				continue
			}
			// Create sets the UTF-8 flag for non-ASCII names, so Thai names unzip correctly.
			zf, err := zw.Create(name)
			if err == nil {
				_, err = io.Copy(zf, f)
			}
			f.Close()
			if err != nil {
				log.Printf("%s zip %s failed: %v", r.RemoteAddr, name, err)
				break // headers are sent; can't change the status now
			}
			n++
		}
		zw.Close()
		log.Printf("%s zipped %d files", r.RemoteAddr, n)
	})
	mux.HandleFunc("POST /f/{name}/rename", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if !isRegular(root, name) {
			http.NotFound(w, r)
			return
		}
		dst, err := newName(name, r.PostFormValue("to"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Windows Rename silently replaces an existing file, so check first.
		// SameFile means a case-only rename (a.pdf -> A.pdf), which is fine.
		if dstInfo, err := root.Lstat(dst); err == nil {
			srcInfo, err := root.Lstat(name)
			if err != nil || !os.SameFile(srcInfo, dstInfo) {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.WriteHeader(http.StatusConflict)
				io.WriteString(w, "มีไฟล์ชื่อนี้อยู่แล้ว") // not http.Error: it appends a newline
				return
			}
		}
		if err := root.Rename(name, dst); err != nil {
			log.Printf("%s rename %s -> %s failed: %v", r.RemoteAddr, name, dst, err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("%s renamed %s -> %s", r.RemoteAddr, name, dst)
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

// newName validates the user-supplied name to for renaming src and keeps src's extension.
// The error text is Thai and shown to the user as-is.
func newName(src, to string) (string, error) {
	to = strings.TrimSpace(to)
	switch {
	case to == "":
		return "", errors.New("กรุณาใส่ชื่อไฟล์")
	case to == "." || to == "..", strings.HasSuffix(to, "."):
		return "", errors.New("ชื่อไฟล์ห้ามเป็น . หรือ .. และห้ามลงท้ายด้วยจุด")
	case strings.ContainsFunc(to, func(r rune) bool { return r < 0x20 || strings.ContainsRune(`<>:"|?*/\`, r) }):
		return "", errors.New(`ชื่อไฟล์ห้ามมีอักขระ < > : " | ? * / \ หรืออักขระควบคุม`)
	}
	ext := filepath.Ext(src)
	if strings.EqualFold(to, ext) {
		return "", errors.New("กรุณาใส่ชื่อไฟล์ก่อนนามสกุล")
	}
	if !strings.HasSuffix(strings.ToLower(to), strings.ToLower(ext)) {
		to += ext
	}
	return to, nil
}

// zipName returns the download name for a zip made at t.
func zipName(t time.Time) string { return t.Format("scans-20060102-1504.zip") }

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
