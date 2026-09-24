@echo off
REM ===========================================================
REM  CricketStream - First-time setup wizard (from source)
REM  Same wizard as CricketStreamSetup.exe, for people who
REM  already have Python. Installs packages and creates
REM  config.ini. Run this once.
REM ===========================================================
setlocal
chcp 65001 >nul
cd /d "%~dp0"

if exist setup_wizard.py goto found
if exist "..\setup_wizard.py" cd ..
if exist setup_wizard.py goto found

echo.
echo   ============================================================
echo    PROBLEM: I can't find the CricketStream project files
echo   ============================================================
echo.
echo    Looked in:  %~dp0
echo    ...and the folder above it. Neither has setup_wizard.py.
echo.
echo    TO FIX: keep this file inside the folder you unzipped -
echo    either in the Windows\ folder, or next to setup_wizard.py
echo    and server.py.
echo.
pause
exit /b 1

:found
REM A fresh Windows 11 ships 0-byte "App Execution Alias" stubs for python.exe that
REM print a Microsoft Store advert and EXIT 0 -- so an errorlevel check reports Python
REM as present when there is none. Only a real interpreter produces output here.
set PYOK=
for /f "delims=" %%i in ('python -c "import sys;print(sys.version_info[0])" 2^>nul') do set PYOK=%%i
if not "%PYOK%"=="3" goto nopython

python setup_wizard.py %*
REM The wizard pauses on its own for every outcome it knows about; this
REM catches the ones it can't (e.g. Python itself failing to start).
if errorlevel 1 (
    echo.
    echo   Setup exited with an error - the reason is above.
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
echo    This file is the "I already have Python" route. If you
echo    don't have it yet, the easier path is:
echo.
echo      Download CricketStreamSetup.exe from the project's
echo      Releases page - it installs Python for you, then runs
echo      this same wizard.
echo.
echo    Or install it yourself from https://python.org/downloads
echo    and TICK "Add python.exe to PATH" on the first screen.
echo.
echo    If you just installed Python, close this window and open a
echo    new one - PATH changes don't reach windows already open.
echo.
pause
exit /b 1
