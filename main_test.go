package main

import (
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestHandler(t *testing.T, dir string) http.Handler {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return newHandler(root)
}

func writeFile(t *testing.T, path, body string, mod time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
}

func do(h http.Handler, method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func TestListNewestFirst(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	writeFile(t, filepath.Join(dir, "old.pdf"), "o", now.Add(-2*time.Hour))
	writeFile(t, filepath.Join(dir, "new.pdf"), "n", now.Add(-1*time.Hour))
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	rec := do(newTestHandler(t, dir), "GET", "/")
	body := rec.Body.String()
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	iNew, iOld := strings.Index(body, "new.pdf"), strings.Index(body, "old.pdf")
	if iNew < 0 || iOld < 0 || iNew > iOld {
		t.Fatalf("want new.pdf before old.pdf, got:\n%s", body)
	}
	if strings.Contains(body, "subdir") {
		t.Fatal("subdirectory must not be listed")
	}
}

func TestListEmpty(t *testing.T) {
	rec := do(newTestHandler(t, t.TempDir()), "GET", "/")
	if !strings.Contains(rec.Body.String(), "(no files)") {
		t.Fatalf("want (no files), got:\n%s", rec.Body.String())
	}
}

func TestDownload(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.pdf"), "hello", time.Now())

	rec := do(newTestHandler(t, dir), "GET", "/f/a.pdf")
	if rec.Code != 200 || rec.Body.String() != "hello" {
		t.Fatalf("status %d body %q", rec.Code, rec.Body.String())
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") {
		t.Fatalf("Content-Disposition %q", cd)
	}
	if rec := do(newTestHandler(t, dir), "GET", "/f/missing.pdf"); rec.Code != 404 {
		t.Fatalf("missing file: status %d", rec.Code)
	}
}

func TestDownloadIndexHTML(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "index.html"), "page", time.Now())

	rec := do(newTestHandler(t, dir), "GET", "/f/index.html")
	if rec.Code != 200 || rec.Body.String() != "page" {
		t.Fatalf("status %d body %q", rec.Code, rec.Body.String())
	}
}

func TestSpecialFilename(t *testing.T) {
	dir := t.TempDir()
	name := "สแกน #1.pdf"
	writeFile(t, filepath.Join(dir, name), "thai", time.Now())
	h := newTestHandler(t, dir)

	href := "/f/" + url.PathEscape(name)
	if body := do(h, "GET", "/").Body.String(); !strings.Contains(body, `href="`+href+`"`) {
		t.Fatalf("want href %q in:\n%s", href, body)
	}
	rec := do(h, "GET", href)
	if rec.Code != 200 || rec.Body.String() != "thai" {
		t.Fatalf("status %d body %q", rec.Code, rec.Body.String())
	}
	_, params, err := mime.ParseMediaType(rec.Header().Get("Content-Disposition"))
	if err != nil || params["filename"] != name {
		t.Fatalf("filename %q err %v", params["filename"], err)
	}
}

func TestDelete(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.pdf")
	writeFile(t, path, "x", time.Now())
	h := newTestHandler(t, dir)

	if rec := do(h, "DELETE", "/f/a.pdf"); rec.Code != 204 {
		t.Fatalf("status %d", rec.Code)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("file still exists")
	}
	if rec := do(h, "DELETE", "/f/a.pdf"); rec.Code != 404 {
		t.Fatalf("second delete: status %d", rec.Code)
	}
}

func TestSubdirRejected(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	h := newTestHandler(t, dir)

	if rec := do(h, "GET", "/f/sub"); rec.Code != 404 {
		t.Fatalf("GET subdir: status %d", rec.Code)
	}
	if rec := do(h, "DELETE", "/f/sub"); rec.Code != 404 {
		t.Fatalf("DELETE subdir: status %d", rec.Code)
	}
	if _, err := os.Stat(sub); err != nil {
		t.Fatal("subdir was deleted")
	}
}

func TestSubfolderPathRejected(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(dir, "sub", "inner.pdf")
	writeFile(t, inner, "INNER", time.Now())
	h := newTestHandler(t, dir)

	for _, target := range []string{"/f/sub%2Finner.pdf", "/f/sub%5Cinner.pdf"} {
		for _, method := range []string{"GET", "DELETE"} {
			if rec := do(h, method, target); rec.Code != 404 {
				t.Fatalf("%s %s: status %d", method, target, rec.Code)
			}
		}
	}
	if _, err := os.Stat(inner); err != nil {
		t.Fatal("file in subfolder was deleted")
	}
}

func TestLogOutputFallsBackToStderr(t *testing.T) {
	if w := logOutput(filepath.Join(t.TempDir(), "missing", "x.log")); w != os.Stderr {
		t.Fatalf("want os.Stderr for unopenable log, got %v", w)
	}
}

func TestTraversalBlocked(t *testing.T) {
	outer := t.TempDir()
	shared := filepath.Join(outer, "share")
	if err := os.Mkdir(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(outer, "secret.txt")
	writeFile(t, secret, "SECRET", time.Now())
	h := newTestHandler(t, shared)

	for _, method := range []string{"GET", "DELETE"} {
		rec := do(h, method, "/f/..%2fsecret.txt")
		if rec.Code == 200 || rec.Code == 204 || strings.Contains(rec.Body.String(), "SECRET") {
			t.Fatalf("%s traversal: status %d body %q", method, rec.Code, rec.Body.String())
		}
	}
	if _, err := os.Stat(secret); err != nil {
		t.Fatal("secret file was deleted")
	}
}
