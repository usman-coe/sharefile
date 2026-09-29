# sharefile Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A hidden Windows background program that serves a scan folder over HTTP so LAN users can list, download, and delete scans from a browser.

**Architecture:** One Go binary (`main.go`) built with `-H=windowsgui`. `newHandler(*os.Root)` holds all HTTP logic and is tested with `httptest`; `main()` only parses flags, opens the folder with `os.OpenRoot`, and listens. PowerShell scripts register a Scheduled Task (at logon) and a firewall rule.

**Tech Stack:** Go 1.26 standard library only (`net/http`, `html/template`, `os.Root`, `mime`), PowerShell 5.1 (`ScheduledTasks`, `NetSecurity` modules).

**Spec:** `docs/superpowers/specs/2026-09-29-sharefile-design.md`

## Global Constraints

- Go standard library only; no third-party modules.
- No authentication; anyone on the LAN can list, download, delete.
- Delete is permanent (`Remove`), no trash folder.
- Flat folder: only regular files directly inside the shared folder are listed/served/deleted.
- All file access goes through `*os.Root` (blocks `../`, absolute paths, symlink escapes).
- Flags: `-dir` (required, absolute), `-addr` default `:8080`, `-log` (empty → stderr).
- Build: `go build -ldflags "-H=windowsgui" -o sharefile.exe`.
- Scheduled Task name `sharefile`, trigger at logon of any user, `ExecutionTimeLimit` = 0, restart on failure 3× / 1 minute.
- Firewall rule display name `sharefile`, inbound TCP, profiles Private and Domain.

## Review Focus

1. Filenames with Thai characters, spaces, and `#` — link in listing must be percent-escaped and download must work with the name intact in `Content-Disposition`. (Task 1: `TestSpecialFilename`)
2. A subfolder name used as `{name}` — must not produce a directory listing on GET, and must not delete an empty subfolder on DELETE. (Task 1: `TestSubdirRejected`)
3. Traversal via `/f/..%2fsecret.txt` — must not read or delete outside the folder. (Task 1: `TestTraversalBlocked`)
4. A file named `index.html` — `http.ServeFileFS` would redirect it; implementation uses `http.ServeContent` instead. (Task 1: `TestDownloadIndexHTML`)
5. Deleting an already-deleted file (two users, or double click) — second delete returns 404, not 500. (Task 1: `TestDelete`)

---

### Task 1: HTTP server

**Files:**
- Create: `go.mod`
- Create: `.gitignore`
- Overwrite: `main.go` (existing untracked draft is replaced entirely)
- Test: `main_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `sharefile.exe` accepting `-dir <abs path> -addr :<port> -log <path>` (used by Task 2's scripts). Internal: `func newHandler(root *os.Root) http.Handler`.

- [ ] **Step 1: Create module and .gitignore**

Run:
```bash
go mod init sharefile
```

`.gitignore`:
```
sharefile.exe
sharefile.log
```

- [ ] **Step 2: Write the failing tests**

`main_test.go`:
```go
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
```

- [ ] **Step 3: Remove the draft so tests fail for the right reason, then run**

Delete the untracked draft `main.go`, then run:
```bash
go test ./...
```
Expected: FAIL — build error `undefined: newHandler`.

- [ ] **Step 4: Write the implementation**

`main.go`:
```go
// sharefile serves one folder over HTTP so LAN users can list, download and delete files.
package main

import (
	"flag"
	"html/template"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
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

// isRegular reports whether name is a regular file (not a dir or symlink) inside root.
func isRegular(root *os.Root, name string) bool {
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
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
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

func main() {
	dir := flag.String("dir", "", "folder to share (absolute path, required)")
	addr := flag.String("addr", ":8080", "listen address")
	logPath := flag.String("log", "", "log file (default stderr)")
	flag.Parse()

	if *logPath != "" {
		f, err := os.OpenFile(*logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			log.Fatal(err)
		}
		log.SetOutput(f)
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
```

- [ ] **Step 5: Run tests to verify they pass**

Run:
```bash
go vet ./... && go test ./...
```
Expected: `ok  sharefile`

- [ ] **Step 6: Build and smoke test**

Run (PowerShell):
```powershell
go build -ldflags "-H=windowsgui" -o sharefile.exe
$d = New-Item -ItemType Directory -Force "$env:TEMP\sharefile-smoke"
"hi" | Out-File "$d\test.txt"
$p = Start-Process .\sharefile.exe -ArgumentList "-dir `"$d`" -addr :18080 -log `"$d\..\sharefile-smoke.log`"" -PassThru
(Invoke-WebRequest http://localhost:18080/ -UseBasicParsing).Content
Invoke-WebRequest http://localhost:18080/f/test.txt -Method Delete -UseBasicParsing | Select-Object StatusCode
Test-Path "$d\test.txt"
Stop-Process $p
Get-Content "$env:TEMP\sharefile-smoke.log"
```
Expected: page HTML listing `test.txt`; `StatusCode 204`; `False`; log shows `sharing ...` and `... deleted test.txt`. No console window appears.

- [ ] **Step 7: Commit**

```bash
git add go.mod .gitignore main.go main_test.go
git commit -m "feat: HTTP server to list, download and delete files in a folder"
```

---

### Task 2: Install / uninstall scripts

**Files:**
- Create: `install.ps1`
- Create: `uninstall.ps1`

**Interfaces:**
- Consumes: `sharefile.exe` from Task 1 in the same folder as the scripts; its flags `-dir`, `-addr`, `-log`.
- Produces: Scheduled Task `sharefile`, firewall rule `sharefile`.

- [ ] **Step 1: Write install.ps1**

```powershell
#Requires -RunAsAdministrator
# Registers sharefile to start hidden at logon and opens the firewall port.
param(
    [Parameter(Mandatory)][string]$Dir,
    [int]$Port = 8080
)
$ErrorActionPreference = 'Stop'

$exe = Join-Path $PSScriptRoot 'sharefile.exe'
if (-not (Test-Path $exe)) { throw "sharefile.exe not found next to install.ps1" }
if (-not (Test-Path $Dir -PathType Container)) { throw "Folder not found: $Dir" }
$Dir = (Resolve-Path $Dir).Path
# A trailing backslash before the closing quote would escape it (e.g. "C:\Scan\").
$argDir = if ($Dir.EndsWith('\')) { $Dir + '\' } else { $Dir }
$log = Join-Path $PSScriptRoot 'sharefile.log'

$action = New-ScheduledTaskAction -Execute $exe -Argument "-dir `"$argDir`" -addr :$Port -log `"$log`""
$trigger = New-ScheduledTaskTrigger -AtLogOn
$settings = New-ScheduledTaskSettingsSet -ExecutionTimeLimit ([TimeSpan]::Zero) `
    -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1) `
    -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -MultipleInstances IgnoreNew
$principal = New-ScheduledTaskPrincipal -GroupId 'S-1-5-32-545' -RunLevel Limited  # BUILTIN\Users
Register-ScheduledTask -TaskName sharefile -Action $action -Trigger $trigger `
    -Settings $settings -Principal $principal -Force | Out-Null

Remove-NetFirewallRule -DisplayName sharefile -ErrorAction SilentlyContinue
New-NetFirewallRule -DisplayName sharefile -Direction Inbound -Protocol TCP -LocalPort $Port `
    -Action Allow -Profile Private, Domain | Out-Null
if (Get-NetConnectionProfile | Where-Object NetworkCategory -eq 'Public') {
    Write-Warning "Network is 'Public': other PCs cannot connect. Change it to Private in Settings > Network & internet > (your network) > Properties."
}

Start-ScheduledTask -TaskName sharefile
Start-Sleep -Seconds 2
if (-not (Get-Process sharefile -ErrorAction SilentlyContinue)) {
    Write-Warning "sharefile is not running yet; it will start at next logon. Check $log"
}

"Installed. Open from another PC:"
Get-NetIPAddress -AddressFamily IPv4 |
    Where-Object { $_.IPAddress -notlike '127.*' -and $_.IPAddress -notlike '169.254.*' } |
    ForEach-Object { "  http://$($_.IPAddress):$Port" }
```

- [ ] **Step 2: Write uninstall.ps1**

```powershell
#Requires -RunAsAdministrator
# Removes the sharefile Scheduled Task and firewall rule.
Stop-ScheduledTask -TaskName sharefile -ErrorAction SilentlyContinue
Unregister-ScheduledTask -TaskName sharefile -Confirm:$false -ErrorAction SilentlyContinue
Stop-Process -Name sharefile -ErrorAction SilentlyContinue
Remove-NetFirewallRule -DisplayName sharefile -ErrorAction SilentlyContinue
"sharefile removed"
```

- [ ] **Step 3: Syntax check without admin**

Run (PowerShell):
```powershell
foreach ($f in 'install.ps1','uninstall.ps1') { $e = $null; [System.Management.Automation.Language.Parser]::ParseFile((Resolve-Path $f), [ref]$null, [ref]$e) | Out-Null; "$f errors: $($e.Count)" }
```
Expected: `install.ps1 errors: 0`, `uninstall.ps1 errors: 0`.

- [ ] **Step 4: Manual install test (needs Administrator — done by the user)**

In an elevated PowerShell in the repo folder:
```powershell
.\install.ps1 -Dir "C:\Scan"
Get-ScheduledTask sharefile | Select-Object TaskName, State
Get-Process sharefile
```
Expected: printed `http://<IP>:8080` URLs; task `Running`; process listed. Open the URL from another PC: list, download, delete work. Sign out and back in: the process starts again. Then `.\uninstall.ps1` → `sharefile removed`, `Get-Process sharefile` finds nothing.

- [ ] **Step 5: Commit**

```bash
git add install.ps1 uninstall.ps1
git commit -m "feat: install/uninstall scripts for Scheduled Task and firewall"
```
