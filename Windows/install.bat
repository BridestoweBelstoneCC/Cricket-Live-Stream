@echo off
REM ===========================================================
REM  CricketStream - Install Python packages only
REM  The manual alternative to setup.bat: this installs the
REM  packages but does NOT create config.ini (you'd fill that
REM  in by hand). Most people want setup.bat instead.
REM ===========================================================
setlocal
chcp 65001 >nul
cd /d "%~dp0"

if exist requirements.txt goto found
if exist "..\requirements.txt" cd ..
if exist requirements.txt goto found

echo.
echo   ============================================================
echo    PROBLEM: I can't find the CricketStream project files
echo   ============================================================
echo.
echo    Looked in:  %~dp0
echo    ...and the folder above it. Neither has requirements.txt.
echo.
echo    TO FIX: keep this file inside the folder you unzipped -
echo    either in the Windows\ folder, or next to requirements.txt
echo    and server.py.
echo.
pause
exit /b 1

:found
echo.
echo   CricketStream Overlay - Installing requirements
echo   ===============================================
echo.

python --version >nul 2>&1
if errorlevel 1 goto nopython

for /f "tokens=*" %%i in ('python --version') do echo   Found: %%i
echo.

echo   Updating pip...
python -m pip install --upgrade pip --quiet

echo   Installing packages...
echo.
python -m pip install -r requirements.txt --progress-bar on

if errorlevel 1 (
    echo.
    echo   ============================================================
    echo    PROBLEM: Installing the packages failed
    echo   ============================================================
    echo.
    echo    The error from pip is printed just above - scroll up.
    echo.
    echo    Most common causes:
    echo      - No internet, or a firewall blocking pip
    echo      - Needs admin rights: right-click this file and choose
    echo        "Run as administrator", then try again
    echo.
    pause
    exit /b 1
)

echo.
echo   ===============================================
echo    All packages installed successfully.
echo    Next: run setup.bat to create config.ini,
echo    or quickstart.bat if you already have one.
echo   ===============================================
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
