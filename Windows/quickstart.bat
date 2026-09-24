@echo off
REM ===========================================================
REM  CricketStream - Match day start
REM  Double-click this. It finds today's fixture, starts the
REM  server and runs a pre-flight check.
REM
REM  This file works whether it's sitting in the Windows\ folder
REM  or copied up next to quickstart.py - it looks in both. Same
REM  search as start_scorer_agent.bat.
REM ===========================================================
setlocal
chcp 65001 >nul
cd /d "%~dp0"

if exist quickstart.py goto found
if exist "..\quickstart.py" cd ..
if exist quickstart.py goto found

echo.
echo   ============================================================
echo    PROBLEM: I can't find the CricketStream project files
echo   ============================================================
echo.
echo    Looked in:  %~dp0
echo    ...and the folder above it. Neither has quickstart.py.
echo.
echo    TO FIX: keep this file inside the folder you unzipped -
echo    either in the Windows\ folder, or next to quickstart.py
echo    and server.py. Moving it somewhere else on its own
echo    (Desktop, Downloads) won't work.
echo.
pause
exit /b 1

:found
echo.
echo   CricketStream Overlay - Quick Start
echo   ===================================
echo.

REM A fresh Windows 11 ships 0-byte "App Execution Alias" stubs for python.exe that
REM print a Microsoft Store advert and EXIT 0 -- so an errorlevel check reports Python
REM as present when there is none. Only a real interpreter produces output here.
set PYOK=
for /f "delims=" %%i in ('python -c "import sys;print(sys.version_info[0])" 2^>nul') do set PYOK=%%i
if not "%PYOK%"=="3" goto nopython

python quickstart.py %*
if errorlevel 1 (
    echo.
    echo   ------------------------------------------------------------
    echo    Quickstart stopped with an error. The reason is printed
    echo    above - scroll up to read it.
    echo.
    echo    First time here? Run setup.bat first: it installs the
    echo    packages and creates config.ini.
    echo   ------------------------------------------------------------
)
echo.
pause
exit /b 0

:nopython
echo.
echo   ============================================================
echo    PROBLEM: Python isn't installed (or isn't on your PATH)
echo   ============================================================
echo.
echo    TO FIX, either:
echo      - Run CricketStreamSetup.exe, which installs Python for
echo        you and then walks through setup, OR
echo      - Install it yourself from https://python.org/downloads
echo        and TICK "Add python.exe to PATH" on the first screen.
echo.
echo    If you just installed Python, close this window and open a
echo    new one - PATH changes don't reach windows already open.
echo.
pause
exit /b 1
