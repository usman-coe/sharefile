@echo off
rem Double-click, or drop the folder to share onto this file. Avoids the .ps1 execution policy block.
net session >nul 2>&1 || (
    powershell -NoProfile -Command "Start-Process -FilePath '%~f0' -ArgumentList '\"%~1\"' -Verb RunAs"
    exit /b
)
set "DIR=%~1"
if not defined DIR set /p "DIR=Drag the folder to share into this window, then press Enter: "
if not defined DIR goto :eof
set "DIR=%DIR:"=%"
rem A trailing backslash would escape the closing quote below.
if "%DIR:~-1%"=="\" set "DIR=%DIR%."
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0install.ps1" -Dir "%DIR%"
pause
