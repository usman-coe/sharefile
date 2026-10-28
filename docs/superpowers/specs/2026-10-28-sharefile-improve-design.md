# sharefile improvements — Design

Date: 2026-10-28
Builds on: `2026-09-29-sharefile-design.md` (read it first; everything there still holds unless changed below).

## Why

Staff scan documents in batches (e.g. `Scan_0001`…`Scan_0010` within five minutes)
and then have to download them one at a time from `http://192.168.1.134/`. That is
the main pain. Scanner names (`Scan_00xx.pdf`) say nothing about content, so staff
also can't tell which file is which without downloading it. The page is a bare
English table that is awkward on phones.

Observed on the live server: every file is a PDF, 250 KB – 1.6 MB, a mix of
`Scan_00xx.pdf` and Thai names (`สรุปวันลา บุษบากร.pdf`).

Success: a staff member ticks a batch, taps once, and gets one `.zip`; can open a
scan to see what it is before picking; can rename it to something meaningful;
can do all of that comfortably on a phone.

## Goals

1. **Multi-select + download as .zip** — the core request.
2. **Preview** — view a PDF in the page before downloading.
3. **Rename** — manual rename from the page (stand-in for auto-naming, see non-goals).
4. **Bulk delete** of the selected files.
5. **New UI**, Thai language, works well on phones.

## Non-goals

- **Automatic naming from document content** (OCR or cloud AI). Discussed and
  deferred to a separate project: local Thai OCR is unreliable on scans, and cloud
  AI would send documents (some containing personal data, e.g. leave summaries)
  outside the office. Manual rename covers the need for now.
- **Thumbnails of the first page** (layout "B" was shown and rejected; would need pdf.js).
- **In-page PDF preview on Android.** Android Chrome can't render PDFs inline;
  the user accepted that Android just downloads the file.
- Auth, subfolders, uploads, trash/undo — unchanged from the original design.

## Anti-goals

- Adding dependencies, a JS framework, a build step, or anything that needs internet.
  Still Go standard library only, single `sharefile.exe`.
- Thai filenames turning into mojibake inside the zip or after rename.
- Rename or zip silently overwriting / losing a file.

## Decided by the user

- Zip = "compress": selected files bundled into one `.zip`.
- Layout **A (list)** from the mockup (kept at `.superpowers/brainstorm/*/content/layout.html`, not committed):
  rows grouped by day, newest first; each row: checkbox, PDF icon, name,
  time + size, "ดู" (view) and "⋯" menu; a bottom action bar appears when
  anything is selected showing count + total size with **ลบ** and **ดาวน์โหลด .zip**.
- Android falls back to download instead of preview.
- Auto-naming deferred.

## The UI (layout A)

- All text in Thai. Sizes human-readable (`673 KB`, `1.5 MB`). Times `09:31`.
- Day group headers: `วันนี้ · 28 ต.ค.`, `เมื่อวาน · 27 ต.ค.`, otherwise `15 ต.ค.`;
  add the Buddhist-era year (`17 ส.ค. 2568`) when not the current year. *(my call)*
- Search box at top filters rows client-side by name as you type. *(my call)*
- Select: per-row checkbox, a "select all" for each day group, and a global select all.
  Bottom bar: `เลือก N ไฟล์ · X MB` + ลบ + ดาวน์โหลด .zip.
  If exactly one file is selected, the download button fetches that file directly, not a zip. *(my call)*
- Tap target ≥ 44px; on narrow screens the "ดู" text collapses and tapping the name opens preview.
- "⋯" menu: ดาวน์โหลด, เปลี่ยนชื่อ, ลบ.
- Empty folder: friendly Thai empty state instead of "(no files)".
- Visual polish is left to the builder (use the `frontend-design` skill), keeping the
  mockup's structure. Fonts must be system fonts (no web-font download): e.g.
  `"Leelawadee UI", "Noto Sans Thai", "Sarabun", sans-serif`.

## Preview

- "ดู" opens a full-screen overlay (`<dialog>`) containing an `<iframe>` of the PDF,
  with close, download, and previous/next buttons to step through the visible list
  (prev/next is *my call*). Esc / back closes it.
- Inline is served by a new query: `GET /f/{name}?inline=1` → same as download but
  `Content-Disposition: inline` (still built with `mime.FormatMediaType`) and the
  correct `Content-Type`. Without `?inline=1`, behaviour is unchanged (attachment).
- If `navigator.pdfViewerEnabled === false` (Android Chrome), "ดู" just downloads the file.
- `?inline=1` is honoured only for `.pdf` (case-insensitive); any other file is still
  served as attachment, so an uploaded `.html` can't run in the page's origin. *(my call)*

## Zip download

- `POST /zip`, form field `name` repeated once per file (a plain `<form method=post>`
  submitted by JS, so the browser handles the download natively, including on phones).
- Response: `Content-Type: application/zip`,
  `Content-Disposition: attachment; filename=scans-YYYYMMDD-HHMM.zip` (server local time).
- Stream with `archive/zip` straight to the response; no temp file. Default `Create`
  (Deflate) is fine — PDFs barely shrink, the point is one file.
- Every name goes through the existing `isRegular` check. Names that fail or vanish
  are skipped and logged; if *none* are valid → 404 before writing anything.
  Duplicate names in the request are included once.
- Zip entries must carry the UTF-8 flag so Thai names extract correctly in
  Windows 11 Explorer. (Go sets it automatically for non-ASCII names — verify
  with a real Thai filename, see Testing.)
- Log `<remote addr> zipped N files`.

## Rename

- `POST /f/{name}/rename` with form field `to` *(route shape is my call)*.
  Success → 204; client reloads/updates the row.
- Validation on the server (trust boundary):
  - source must pass `isRegular`; else 404.
  - `to` trimmed; must be non-empty, a single segment (no `/` `\`), no Windows-invalid
    characters `<>:"|?*` or control chars, not `.`/`..`, not ending in space or dot.
    Invalid → 400 with a Thai message.
  - The original extension is kept: if `to` doesn't end with the source's extension
    (case-insensitive), append it. The UI pre-fills the name without extension.
  - If a file named `to` already exists → 409, nothing changes. (Windows `os.Rename`
    *replaces* existing files, so this check is required. A tiny race between check
    and rename is acceptable for this office.) Renaming to the same name differing only
    in case is allowed: on Windows the exists-check finds the source itself, so treat
    "target exists *and* `os.SameFile` with source" as not a conflict. *(my call)*
- Use `root.Rename` so it stays inside the `*os.Root`.
- Log `<remote addr> renamed <old> -> <new>`.

## Bulk delete

- Client loops the existing `DELETE /f/{name}` for each selected file after one
  confirm (`ลบ N ไฟล์?`). Reports any failures, then reloads. No new server route.

## Constraints

- Go 1.25+, stdlib only; must still build with `-ldflags "-H=windowsgui"`.
- Safety rules from the original design stay: all filesystem access through `*os.Root`,
  single path segment, regular files only.
- Works offline on the LAN; page must work in current Chrome/Edge (desktop + Android)
  and Safari (iPhone).
- `install.ps1` / `uninstall.ps1` need no changes; upgrading = stop task, replace exe, start task.

## Left to the builder

- Whether the HTML stays inline in `main.go` or moves to an embedded file (`//go:embed`).
- Exact visual style within the layout above.
- How the listing data reaches the page (server-rendered rows vs. JSON) as long as
  there is no build step.

## Testing

Extend `main_test.go` (httptest, temp dir):

1. `POST /zip` with two names (one Thai) → valid zip containing exactly those files
   with correct bytes and names; UTF-8 flag set on the Thai entry.
2. `POST /zip` with a traversal name / subdir / missing name → skipped; all invalid → 404.
3. `GET /f/{name}?inline=1` → `Content-Disposition: inline`, PDF content type.
4. Rename: success; target exists → 409 and both files intact; invalid names → 400;
   extension auto-kept; traversal in `to` rejected; missing source → 404.
5. Existing tests keep passing, except `TestListEmpty`'s `(no files)` assertion, which
   changes to the new Thai empty-state text. If rows stop being server-rendered,
   update `TestSpecialFilename`'s `href` check accordingly.

Manual: build, run against a copy of the scan folder, open from a Windows PC, an
iPhone, and an Android phone; download a zip with Thai names and extract it in
Windows Explorer.
