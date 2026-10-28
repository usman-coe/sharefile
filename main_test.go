package main

import (
	"archive/zip"
	"bytes"
	"io"
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
	if !strings.Contains(rec.Body.String(), "ยังไม่มีไฟล์สแกน") {
		t.Fatalf("want empty-state text, got:\n%s", rec.Body.String())
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
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("Cache-Control %q", cc)
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

func TestInlinePDF(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.PDF"), "%PDF", time.Now())

	rec := do(newTestHandler(t, dir), "GET", "/f/a.PDF?inline=1")
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	disp, params, err := mime.ParseMediaType(rec.Header().Get("Content-Disposition"))
	if err != nil || disp != "inline" || params["filename"] != "a.PDF" {
		t.Fatalf("disposition %q filename %q err %v", disp, params["filename"], err)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/pdf" {
		t.Fatalf("content-type %q", ct)
	}
}

func TestInlineNonPDFStaysAttachment(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "index.html"), "page", time.Now())

	rec := do(newTestHandler(t, dir), "GET", "/f/index.html?inline=1")
	if cd := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") {
		t.Fatalf("Content-Disposition %q", cd)
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

func postForm(h http.Handler, target string, form url.Values) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(rec, req)
	return rec
}

func TestZipName(t *testing.T) {
	got := zipName(time.Date(2026, 10, 28, 9, 31, 0, 0, time.Local))
	if got != "scans-20261028-0931.zip" {
		t.Fatalf("got %q", got)
	}
}

func TestZip(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	writeFile(t, filepath.Join(dir, "a.pdf"), "aaa", now)
	writeFile(t, filepath.Join(dir, "สแกน #1.pdf"), "thai", now)
	writeFile(t, filepath.Join(dir, "other.pdf"), "x", now)
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	h := newTestHandler(t, dir)

	rec := postForm(h, "/zip", url.Values{"name": {"a.pdf", "สแกน #1.pdf", "a.pdf", "missing.pdf", `..\secret`, "subdir"}})
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("Content-Type %q", ct)
	}
	_, params, err := mime.ParseMediaType(rec.Header().Get("Content-Disposition"))
	if err != nil {
		t.Fatal(err)
	}
	if fn := params["filename"]; !strings.HasPrefix(fn, "scans-") || !strings.HasSuffix(fn, ".zip") {
		t.Fatalf("filename %q", fn)
	}

	body := rec.Body.Bytes()
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		got[f.Name] = string(b)
		if f.Name == "สแกน #1.pdf" && (f.NonUTF8 || f.Flags&0x800 == 0) {
			t.Fatalf("Thai entry not flagged UTF-8: NonUTF8=%v flags=%#x", f.NonUTF8, f.Flags)
		}
	}
	if len(zr.File) != 2 || got["a.pdf"] != "aaa" || got["สแกน #1.pdf"] != "thai" {
		t.Fatalf("entries: %d %v", len(zr.File), got)
	}
}

func TestZipEntryModTime(t *testing.T) {
	dir := t.TempDir()
	mod := time.Date(2026, 3, 4, 10, 20, 30, 0, time.Local)
	writeFile(t, filepath.Join(dir, "a.pdf"), "aaa", mod)
	rec := postForm(newTestHandler(t, dir), "/zip", url.Values{"name": {"a.pdf"}})
	body := rec.Body.Bytes()
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil || len(zr.File) != 1 {
		t.Fatalf("zip: %v, %d entries", err, len(zr.File))
	}
	if got := zr.File[0].Modified; got.Sub(mod).Abs() > 2*time.Second {
		t.Fatalf("Modified %v, want %v", got, mod)
	}
}

func TestCrossSitePostRejected(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.pdf"), "x", time.Now())
	h := newTestHandler(t, dir)
	post := func(site string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/f/a.pdf/rename", strings.NewReader("to=b"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Sec-Fetch-Site", site)
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := post("cross-site"); code != 403 {
		t.Fatalf("cross-site: status %d", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.pdf")); err != nil {
		t.Fatal("cross-site request changed files")
	}
	if code := post("same-origin"); code != 204 {
		t.Fatalf("same-origin: status %d", code)
	}
}

func TestZipNoValidNames(t *testing.T) {
	h := newTestHandler(t, t.TempDir())
	for _, form := range []url.Values{{}, {"name": {"missing.pdf"}}, {"name": {"../x"}}} {
		if rec := postForm(h, "/zip", form); rec.Code != 404 {
			t.Fatalf("form %v: status %d", form, rec.Code)
		}
	}
}

func TestNewName(t *testing.T) {
	ok := map[[2]string]string{
		{"Scan_0001.pdf", "สรุปวันลา"}:    "สรุปวันลา.pdf",
		{"Scan_0001.pdf", " รายงาน.PDF "}: "รายงาน.PDF",
		{"Scan_0001.pdf", "ประชุม 1.2"}:   "ประชุม 1.2.pdf",
	}
	for in, want := range ok {
		if got, err := newName(in[0], in[1]); err != nil || got != want {
			t.Errorf("newName(%q, %q) = %q, %v; want %q", in[0], in[1], got, err, want)
		}
	}
	if got, err := newName("Scan_0001.pdf", strings.Repeat("ก", 150)); err != nil || got != strings.Repeat("ก", 150)+".pdf" {
		t.Errorf("150 runes: %q, %v", got, err)
	}
	bad := []string{strings.Repeat("ก", 201), "", "   ", "a/b", `a\b`, "a:b", `a"b`, "a<b", "a>b", "a|b", "a?b", "a*b", "a\x01b", ".", "..", "abc.", ".pdf"}
	for _, to := range bad {
		if got, err := newName("Scan_0001.pdf", to); err == nil {
			t.Errorf("newName(%q) = %q, want error", to, got)
		}
	}
}

func TestRename(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	writeFile(t, filepath.Join(dir, "Scan_0001.pdf"), "s", now)
	writeFile(t, filepath.Join(dir, "taken.pdf"), "t", now)
	h := newTestHandler(t, dir)
	read := func(name string) string {
		b, _ := os.ReadFile(filepath.Join(dir, name))
		return string(b)
	}

	if rec := postForm(h, "/f/Scan_0001.pdf/rename", url.Values{"to": {"สแกน #1"}}); rec.Code != 204 {
		t.Fatalf("rename: status %d", rec.Code)
	}
	if read("สแกน #1.pdf") != "s" {
		t.Fatal("renamed file missing or wrong body")
	}
	if _, err := os.Stat(filepath.Join(dir, "Scan_0001.pdf")); err == nil {
		t.Fatal("old name still exists")
	}

	rec := postForm(h, "/f/"+url.PathEscape("สแกน #1.pdf")+"/rename", url.Values{"to": {"taken"}})
	if rec.Code != 409 || rec.Body.String() != "มีไฟล์ชื่อนี้อยู่แล้ว" {
		t.Fatalf("conflict: status %d body %q", rec.Code, rec.Body.String())
	}
	if read("สแกน #1.pdf") != "s" || read("taken.pdf") != "t" {
		t.Fatal("conflict changed files")
	}

	if rec := postForm(h, "/f/"+url.PathEscape("สแกน #1.pdf")+"/rename", url.Values{"to": {"a/b"}}); rec.Code != 400 {
		t.Fatalf("invalid to: status %d", rec.Code)
	}
	for _, src := range []string{"missing.pdf", "..%2fx"} {
		if rec := postForm(h, "/f/"+src+"/rename", url.Values{"to": {"ok"}}); rec.Code != 404 {
			t.Fatalf("%s: status %d", src, rec.Code)
		}
	}
}

func TestRenameCaseOnly(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.pdf"), "x", time.Now())
	h := newTestHandler(t, dir)
	if rec := postForm(h, "/f/a.pdf/rename", url.Values{"to": {"A"}}); rec.Code != 204 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "A.pdf" {
		t.Fatalf("entries: %v %v", entries, err)
	}
}

func TestDayLabel(t *testing.T) {
	now := time.Date(2026, 10, 28, 10, 0, 0, 0, time.Local)
	for _, c := range []struct {
		d    time.Time
		want string
	}{
		{time.Date(2026, 10, 28, 9, 31, 0, 0, time.Local), "วันนี้ · 28 ต.ค."},
		{time.Date(2026, 10, 27, 23, 59, 0, 0, time.Local), "เมื่อวาน · 27 ต.ค."},
		{time.Date(2026, 10, 15, 11, 20, 0, 0, time.Local), "15 ต.ค."},
		{time.Date(2025, 8, 17, 8, 0, 0, 0, time.Local), "17 ส.ค. 2568"},
		{time.Date(2026, 1, 1, 0, 0, 0, 0, time.Local), "1 ม.ค."},
	} {
		if got := dayLabel(c.d, now); got != c.want {
			t.Errorf("dayLabel(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestHumanSize(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 1023: "1023 B", 688771: "673 KB", 1601809: "1.5 MB"} {
		if got := humanSize(n); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestGroupByDay(t *testing.T) {
	now := time.Date(2026, 10, 28, 10, 0, 0, 0, time.Local)
	files := []fileInfo{
		{"c.pdf", 1, time.Date(2026, 10, 28, 9, 31, 0, 0, time.Local)},
		{"b.pdf", 1, time.Date(2026, 10, 28, 9, 30, 0, 0, time.Local)},
		{"a.pdf", 1, time.Date(2026, 10, 15, 11, 20, 0, 0, time.Local)},
	}
	g := groupByDay(files, now)
	if len(g) != 2 || g[0].Label != "วันนี้ · 28 ต.ค." || g[1].Label != "15 ต.ค." ||
		len(g[0].Files) != 2 || len(g[1].Files) != 1 || g[0].Files[0].Name != "c.pdf" || g[0].Files[1].Name != "b.pdf" {
		t.Fatalf("got %+v", g)
	}
}
