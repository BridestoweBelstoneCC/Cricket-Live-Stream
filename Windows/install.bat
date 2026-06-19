@echo off
cd /d %~dp0
echo.
echo  BBCC Stream Overlay - Installing requirements
echo  ===============================================
echo.

:: Check Python
python --version >nul 2>&1
if errorlevel 1 (
    echo  ERROR: Python not found.
    echo.
    echo  1. Go to https://python.org/downloads
    echo  2. Download the latest Python installer
    echo  3. Run it and tick "Add Python to PATH"
    echo  4. Restart this installer
    echo.
    pause
    exit /b 1
)

for /f "tokens=*" %%i in ('python --version') do echo  Found: %%i
echo.

:: Upgrade pip silently
echo  Updating pip...
python -m pip install --upgrade pip --quiet

:: Install packages
echo  Installing packages...
echo.
python -m pip install -r requirements.txt --quiet --progress-bar on

if errorlevel 1 (
    echo.
    echo  ERROR: Installation failed.
    echo  Try running this file as Administrator.
    pause
    exit /b 1
)

echo.
echo  ===============================================
echo   All packages installed successfully.
echo   You can now run quickstart.bat
echo  ===============================================
echo.
pause
