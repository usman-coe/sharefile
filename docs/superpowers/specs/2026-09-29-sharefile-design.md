# sharefile — Design

Date: 2026-09-29

## Purpose

A USB scanner (no network sharing) saves scans into a folder on a staff member's
Windows PC. `sharefile` lets anyone on the LAN open `http://<PC-IP>:8080` in a
browser, download those scans, and delete them — without interrupting the staff
member at that PC.

## Decisions

| Topic | Decision |
|---|---|
| Client | Web browser only |
| Background mode | Scheduled Task at user logon, hidden window (not a Windows Service) |
| Auth | None — anyone on the LAN can list, download, delete |
| Delete | Permanent (`os.Remove`), no trash folder |
| Folder layout | Flat — only regular files directly inside the shared folder |
| Dependencies | Go standard library only |

## Files

```
go.mod
main.go         HTTP server
main_test.go    httptest-based tests
install.ps1     register Scheduled Task + firewall rule, start it
uninstall.ps1   remove task + firewall rule
```

## Program: `sharefile.exe`

Build: `go build -ldflags "-H=windowsgui" -o sharefile.exe` (no console window).

Flags:

- `-dir` (required) — absolute path of the scan folder. Missing → exit with error.
- `-addr` — listen address, default `:8080`.
- `-log` — log file path. Empty → stderr. Used because the hidden build has no console.

Startup failures (folder missing, port in use) are logged and the process exits.

### Routes

| Method & path | Behavior |
|---|---|
| `GET /` | HTML table of regular files: name, size, modified time, newest first. Each row: download link, delete button with `confirm()` that sends `DELETE` via `fetch` then reloads. Empty folder shows "(no files)". |
| `GET /f/{name}` | Download file. `Content-Disposition: attachment` built with `mime.FormatMediaType` so Thai / non-ASCII names work. Missing → 404. |
| `DELETE /f/{name}` | Permanently remove file. Log `<remote addr> deleted <name>`. Missing → 404. Success → 204. |

### Safety

- The folder is opened with `os.OpenRoot`; every read/delete goes through the
  `*os.Root`, so `../`, absolute paths, and symlink escapes are rejected.
- `{name}` is a single path segment; subfolders are neither listed nor served.
- Only regular files appear in the listing.

### Known limitation

A file still being written by the scanner can be downloaded incomplete. Not
handled in code: the modified-time column lets the user wait a moment.

## Install: `install.ps1 -Dir <path> [-Port 8080]`

Run once as Administrator from the folder containing `sharefile.exe`.

1. Register Scheduled Task `sharefile`:
   - Trigger: at logon of any user.
   - Action: `sharefile.exe -dir <Dir> -addr :<Port> -log <exe folder>\sharefile.log`.
   - Settings: `ExecutionTimeLimit` = 0 (default would kill it after 72h),
     restart on failure (3 tries, 1 minute apart), allow start on battery.
2. Firewall rule `sharefile` — inbound TCP `<Port>`, profiles Private and Domain.
   Print a warning if the active network profile is Public.
3. Start the task immediately and print `http://<IPv4>:<Port>` for each LAN address.

`uninstall.ps1` stops and unregisters the task and removes the firewall rule.

## Testing

`main_test.go` (httptest, temp dir):

1. Listing orders files newest first and skips subdirectories.
2. `GET /f/{name}` returns the file's bytes.
3. `DELETE /f/{name}` removes the file; second delete → 404.
4. Traversal (`/f/..%2fsecret`) does not read or delete outside the folder.

Manual: build, run against a real folder, open the page from another machine.
