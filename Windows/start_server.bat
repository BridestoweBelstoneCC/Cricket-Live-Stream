@echo off
REM ===========================================================
REM  CricketStream - Start the server only
REM  quickstart.bat is the normal match-day route (it also does
REM  the fixture lookup and pre-flight checks). Use this one if
REM  you just want the bare server running.
REM ===========================================================
setlocal
chcp 65001 >nul
cd /d "%~dp0"

if exist server.py goto found
if exist "..\server.py" cd ..
if exist server.py goto found

echo.
echo   ============================================================
echo    PROBLEM: I can't find the CricketStream project files
echo   ============================================================
echo.
echo    Looked in:  %~dp0
echo    ...and the folder above it. Neither has server.py.
echo.
echo    TO FIX: keep this file inside the folder you unzipped -
echo    either in the Windows\ folder, or next to server.py.
echo.
pause
exit /b 1

:found
REM A fresh Windows 11 ships 0-byte "App Execution Alias" stubs for python.exe that
REM print a Microsoft Store advert and EXIT 0 -- so an errorlevel check reports Python
REM as present when there is none. Only a real interpreter produces output here.
set PYOK=
for /f "delims=" %%i in ('python -c "import sys;print(sys.version_info[0])" 2^>nul') do set PYOK=%%i
if not "%PYOK%"=="3" (
    echo.
    echo   ============================================================
    echo    PROBLEM: Python isn't installed (or isn't on your PATH)
    echo   ============================================================
    echo.
    echo    Run CricketStreamSetup.exe, or install Python from
    echo    https://python.org/downloads and TICK "Add python.exe
    echo    to PATH" on the first screen.
    echo.
    pause
    exit /b 1
)

echo   Starting CricketStream server...
echo   Control panel: http://localhost:5000/control
echo   Press Ctrl+C in this window to stop it.
echo.
python server.py %*
if errorlevel 1 (
    echo.
    echo   ------------------------------------------------------------
    echo    The server stopped with an error - the reason is above.
    echo.
    echo    No config.ini yet? Run setup.bat first.
    echo   ------------------------------------------------------------
)
echo.
pause
exit /b 0
