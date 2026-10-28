# sharefile improvements Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add multi-select zip download, PDF preview, rename, bulk delete and a Thai mobile-friendly UI to `sharefile`.

**Architecture:** Same single Go binary. Three new server behaviours (`?inline=1`, `POST /zip`, `POST /f/{name}/rename`) in `main.go`, all going through the existing `*os.Root` + `isRegular`. The page moves to an embedded `page.html` (server-rendered rows grouped by day, vanilla JS for selection/preview/rename/bulk delete).

**Tech Stack:** Go 1.25+ standard library only (`archive/zip`, `embed`, `html/template`, `net/http`). No JS libraries, no build step, no web fonts.

**Spec:** `docs/superpowers/specs/2026-10-28-sharefile-improve-design.md` (builds on `docs/superpowers/specs/2026-09-29-sharefile-design.md`).

## Global Constraints

- Go standard library only; must build with `go build -ldflags "-H=windowsgui" -o sharefile.exe`.
- All filesystem access through `*os.Root`; every incoming name passes `isRegular` (single segment, regular file).
- All UI text Thai. System fonts only: `"Leelawadee UI", "Noto Sans Thai", "Sarabun", sans-serif`.
- Must work offline in current Chrome/Edge (desktop + Android) and Safari (iPhone). Tap targets ≥ 44px.
- `install.ps1` / `uninstall.ps1` unchanged.
- Log formats: `<remote addr> zipped N files`, `<remote addr> renamed <old> -> <new>` (existing `deleted` log stays).

## Review Focus

1. **Thai names with spaces/`#` through form posts** (zip `name`, rename `to`) must round-trip exactly — tests in Task 2 and Task 3 use `สแกน #1.pdf`.
2. **Case-only rename on Windows** (`a.pdf` → `A.pdf`) must succeed, not 409 — test in Task 3.
3. **Extension typed in a different case** (`to=รายงาน.PDF` for `x.pdf`) must not become `รายงาน.PDF.pdf` — test in Task 3.
4. **Zip request with zero valid names** (none sent, all missing, all traversal) → 404 with nothing streamed — test in Task 2.
5. **Non-PDF `?inline=1`** (e.g. `index.html`) must still download as attachment — test in Task 1.

---

### Task 1: Inline PDF preview

**Files:**
- Modify: `main.go` (the `GET /f/{name}` handler)
- Test: `main_test.go`

**Interfaces:**
- Produces: `GET /f/{name}?inline=1` → `Content-Disposition: inline; filename=...` for `.pdf` (case-insensitive ext); unchanged attachment otherwise. Used by Task 5's preview iframe.

- [ ] **Step 1: Write the failing tests**

```go
func TestInlinePDF(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.PDF"), "%PDF", time.Now())
	rec := do(newTestHandler(t, dir), "GET", "/f/a.PDF?inline=1")
	// assert: rec.Code == 200
	// assert: Content-Disposition parses (mime.ParseMediaType) to disposition "inline", filename "a.PDF"
	// assert: Content-Type == "application/pdf"
}

func TestInlineNonPDFStaysAttachment(t *testing.T) {
	// index.html with ?inline=1 → Content-Disposition starts with "attachment"
}
```

- [ ] **Step 2: Run** `go test -run 'TestInline' ./...` — Expected: FAIL (disposition is attachment).
- [ ] **Step 3: Implement** — in the handler pick `"inline"` when `r.URL.Query().Get("inline") == "1"` and `strings.EqualFold(filepath.Ext(name), ".pdf")`, else `"attachment"`; pass it to the existing `mime.FormatMediaType` call. `ServeContent` already sets `application/pdf` from the extension.
- [ ] **Step 4: Run** `go test ./...` — Expected: PASS (all, including old tests).
- [ ] **Step 5: Commit** `git commit -am "feat: inline PDF preview via ?inline=1"`

### Task 2: Zip download

**Files:**
- Modify: `main.go` (new route in `newHandler`, new func)
- Test: `main_test.go`

**Interfaces:**
- Produces: `POST /zip` with form field `name` repeated (`application/x-www-form-urlencoded`). Response `200`, `Content-Type: application/zip`, `Content-Disposition: attachment; filename=scans-YYYYMMDD-HHMM.zip`. `404` if no valid names. Used by Task 5.
- Produces: `func zipName(t time.Time) string` → e.g. `scans-20261028-0931.zip`.
- Produces test helper `postForm(h http.Handler, target string, form url.Values) *httptest.ResponseRecorder` (sets `Content-Type: application/x-www-form-urlencoded`). Reused by Task 3.

- [ ] **Step 1: Write the failing tests**

```go
func TestZipName(t *testing.T) {
	// zipName(time.Date(2026, 10, 28, 9, 31, 0, 0, time.Local)) == "scans-20261028-0931.zip"
}

func TestZip(t *testing.T) {
	// dir: "a.pdf"="aaa", "สแกน #1.pdf"="thai", "other.pdf"="x"
	// postForm(h, "/zip", url.Values{"name": {"a.pdf", "สแกน #1.pdf", "a.pdf", "missing.pdf", "..\\secret", "subdir"}})
	//   (create subdir as a directory)
	// assert: 200, Content-Type "application/zip", disposition filename has prefix "scans-" and suffix ".zip"
	// read with zip.NewReader(bytes.NewReader(body), len): exactly 2 entries,
	//   names {"a.pdf","สแกน #1.pdf"} with bodies "aaa","thai" (duplicate included once, others skipped)
	// assert: Thai entry has f.NonUTF8 == false and f.Flags&0x800 != 0
}

func TestZipNoValidNames(t *testing.T) {
	// three cases → 404 each: no fields; {"missing.pdf"}; {"../x"}
}
```

- [ ] **Step 2: Run** `go test -run 'TestZip' ./...` — Expected: FAIL (zipName undefined / 405).
- [ ] **Step 3: Implement** `zipName` and the `POST /zip` handler. Approach: `r.ParseForm()`, dedupe `r.PostForm["name"]` keeping order, filter with `isRegular`; if empty → `http.NotFound` before writing headers. Set headers (disposition via `mime.FormatMediaType`), `zip.NewWriter(w)`; per file: `root.Open` (skip + log on error), `zw.Create(name)`, `io.Copy`; log copy errors and stop. `zw.Close()`. Log `<remote addr> zipped N files`.
- [ ] **Step 4: Run** `go test ./...` — Expected: PASS.
- [ ] **Step 5: Commit** `git commit -am "feat: download selected files as zip"`

### Task 3: Rename

**Files:**
- Modify: `main.go`
- Test: `main_test.go`

**Interfaces:**
- Consumes: `postForm` (Task 2).
- Produces: `POST /f/{name}/rename` with form field `to`. `204` success; `400` invalid `to` (Thai plain-text body); `404` source not regular; `409` target exists (body `มีไฟล์ชื่อนี้อยู่แล้ว`). Used by Task 5.
- Produces: `func newName(src, to string) (string, error)` — trims `to`, validates, appends `filepath.Ext(src)` unless `to` already ends with it case-insensitively. Error text is Thai, shown to user as-is.

- [ ] **Step 1: Write the failing tests**

```go
func TestNewName(t *testing.T) {
	ok := map[[2]string]string{
		{"Scan_0001.pdf", "สรุปวันลา"}:     "สรุปวันลา.pdf",
		{"Scan_0001.pdf", " รายงาน.PDF "}:  "รายงาน.PDF",
		{"Scan_0001.pdf", "ประชุม 1.2"}:    "ประชุม 1.2.pdf",
	}
	bad := []string{"", "   ", "a/b", `a\b`, "a:b", `a"b`, "a<b", "a>b", "a|b", "a?b", "a*b", "a\x01b", ".", "..", "abc."}
	// assert each ok pair maps to its value with nil error; each bad returns non-nil error
	// note: "abc." → ends in "." → error (Windows strips trailing dots); ".pdf" → empty base → error
}

func TestRename(t *testing.T) {
	// dir: "Scan_0001.pdf"="s", "taken.pdf"="t"
	// postForm(h, "/f/Scan_0001.pdf/rename", {"to": {"สแกน #1"}}) → 204;
	//   file "สแกน #1.pdf" has body "s", "Scan_0001.pdf" gone
	// rename "สแกน #1.pdf" (URL path-escaped) to "taken" → 409; both files still have bodies "s" and "t"
	// rename to "a/b" → 400; rename "missing.pdf" → 404; rename "..%2fx" → 404
}

func TestRenameCaseOnly(t *testing.T) {
	// dir: "a.pdf"="x"; rename to "A" → 204; os.ReadDir shows exactly one entry named "A.pdf"
}
```

- [ ] **Step 2: Run** `go test -run 'TestNewName|TestRename' ./...` — Expected: FAIL.
- [ ] **Step 3: Implement** `newName` and the handler. Conflict check: `root.Lstat(dst)`; if it exists and `!os.SameFile(srcInfo, dstInfo)` → 409 (SameFile = case-only rename on Windows). Then `root.Rename(src, dst)`; on error → 500 + log. Log `<remote addr> renamed <old> -> <new>`.
- [ ] **Step 4: Run** `go test ./...` — Expected: PASS.
- [ ] **Step 5: Commit** `git commit -am "feat: rename files"`

### Task 4: Server-rendered Thai page, grouped by day

**Files:**
- Create: `page.html` (embedded with `//go:embed page.html`; replaces the inline `page` template string)
- Modify: `main.go`
- Test: `main_test.go`

**Interfaces:**
- Produces: `type dayGroup struct { Label string; Files []fileInfo }`
- Produces: `func groupByDay(files []fileInfo, now time.Time) []dayGroup` — input already newest-first; one group per local calendar day, same order.
- Produces: `func dayLabel(d, now time.Time) string`
- Produces: `func humanSize(n int64) string` — `< 1024` → `"N B"`; `< 1 MiB` → whole KB rounded (`"673 KB"`); else one decimal MB (`"1.5 MB"`).
- Produces: template data `{{range .}}` over `[]dayGroup`. Each file row must keep `<a href="/f/{{pathEscape .Name}}">` and carry `data-name="{{.Name}}"` and `data-size="{{.Size}}"` on the row element, a checkbox per row, and a per-group select-all checkbox — Task 5 hooks onto these. Empty state text: `ยังไม่มีไฟล์สแกน`.

- [ ] **Step 1: Write the failing tests**

```go
func TestDayLabel(t *testing.T) {
	now := time.Date(2026, 10, 28, 10, 0, 0, 0, time.Local)
	// dayLabel(now at 09:31, now)            == "วันนี้ · 28 ต.ค."
	// dayLabel(2026-10-27 23:59, now)        == "เมื่อวาน · 27 ต.ค."
	// dayLabel(2026-10-15, now)              == "15 ต.ค."
	// dayLabel(2025-08-17, now)              == "17 ส.ค. 2568"
	// dayLabel(2026-01-01, now)              == "1 ม.ค."
}

func TestHumanSize(t *testing.T) {
	// 0 → "0 B"; 1023 → "1023 B"; 688771 → "673 KB"; 1601809 → "1.5 MB"
}

func TestGroupByDay(t *testing.T) {
	// 3 files: two on 2026-10-28, one on 2026-10-15 (newest first)
	// → 2 groups, labels "วันนี้ · 28 ต.ค." and "15 ต.ค.", sizes 2 and 1, order preserved
}
```

Also change `TestListEmpty` to expect `ยังไม่มีไฟล์สแกน` instead of `(no files)`.

- [ ] **Step 2: Run** `go test ./...` — Expected: FAIL (undefined funcs, empty-state text).
- [ ] **Step 3: Implement** the three funcs (Thai month abbreviations `ม.ค. ก.พ. มี.ค. เม.ย. พ.ค. มิ.ย. ก.ค. ส.ค. ก.ย. ต.ค. พ.ย. ธ.ค.`; Buddhist year = `year+543`, shown only when year ≠ `now`'s year), move markup to `page.html` with `<html lang="th">`, `<meta name="viewport" content="width=device-width, initial-scale=1">`, register `humanSize` in the template FuncMap, render time as `{{.ModTime.Format "15:04"}}`. Markup only in this task — layout A structure (header + search input, group headers, rows, hidden bottom bar) with CSS; no behaviour yet beyond the existing delete.
- [ ] **Step 4: Run** `go test ./...` — Expected: PASS (`TestListNewestFirst`, `TestSpecialFilename` still pass unchanged).
- [ ] **Step 5: Commit** `git add page.html && git commit -am "feat: Thai page grouped by day"`

### Task 5: Client behaviour and visual polish

**Files:**
- Modify: `page.html`

**Interfaces:**
- Consumes: `?inline=1` (Task 1), `POST /zip` (Task 2), `POST /f/{name}/rename` (Task 3), row `data-name`/`data-size` + checkboxes (Task 4), existing `DELETE /f/{name}`.

Use the `frontend-design:frontend-design` skill for the visual pass, keeping layout A from the mockup (`.superpowers/brainstorm/*/content/layout.html` if present; otherwise the spec's "The UI" section).

- [ ] **Step 1: Selection + bottom bar** — row checkbox, per-group and global select-all; bar shows `เลือก N ไฟล์ · <humanSize total>` (JS port of `humanSize`), hidden when N = 0.
- [ ] **Step 2: Download** — N = 1 → navigate to `/f/<encoded name>`; N > 1 → build and submit a hidden `<form method="post" action="/zip">` with one `name` input per file.
- [ ] **Step 3: Bulk delete** — one `confirm('ลบ ' + N + ' ไฟล์?')`, sequential `DELETE` per file, `alert` listing any failures, then `location.reload()`.
- [ ] **Step 4: Preview** — "ดู" (and tapping the name on narrow screens) opens a `<dialog>` with `<iframe src="/f/<name>?inline=1">`, buttons ปิด / ดาวน์โหลด / ก่อนหน้า / ถัดไป over currently visible rows; Esc closes. If `navigator.pdfViewerEnabled === false` or the file isn't `.pdf`, go straight to download instead.
- [ ] **Step 5: "⋯" menu** — ดาวน์โหลด / เปลี่ยนชื่อ / ลบ. Rename: `prompt('ชื่อใหม่', nameWithoutExt)`, POST `to`; on non-204 `alert(await r.text())`; on 204 reload.
- [ ] **Step 6: Search** — input filters rows by case-insensitive substring of name; group header hidden when all its rows are hidden; select-all only affects visible rows.
- [ ] **Step 7: Verify in a browser** — `go build -o sharefile.exe && ./sharefile.exe -dir <temp copy with a few PDFs incl. Thai names> -addr :8090`; with Playwright at 1280×800 and 390×844 check: select 3 → zip downloads and contains 3 files; select 1 → direct download; preview opens/next/prev/close; rename works and 409 shows `มีไฟล์ชื่อนี้อยู่แล้ว`; bulk delete; search; no horizontal scroll on mobile width; no console errors. Run `go test ./...` — PASS.
- [ ] **Step 8: Commit** `git commit -am "feat: selection, zip, preview, rename and bulk delete in UI"`

### Task 6: Release build and handoff

- [ ] **Step 1:** `go vet ./... && go test ./...` — Expected: no output from vet, PASS.
- [ ] **Step 2:** `go build -ldflags "-H=windowsgui" -o sharefile.exe` — Expected: builds.
- [ ] **Step 3:** Extract a zip containing a Thai filename with Windows Explorer (or `Expand-Archive`) — Expected: Thai name intact.
- [ ] **Step 4:** Report to user: upgrade = stop Scheduled Task `sharefile`, replace `sharefile.exe`, start task. **Do not deploy to the live machine without the user's go-ahead.**
