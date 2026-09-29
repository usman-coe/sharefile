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
