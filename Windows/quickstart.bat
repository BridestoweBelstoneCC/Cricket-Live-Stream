@echo off
cd /d %~dp0
echo.
echo  CricketStream Overlay - Quick Start
echo  ===================================
echo.

:: Check Python is installed
python --version >nul 2>&1
if errorlevel 1 (
    echo  ERROR: Python not found.
    echo  Download from https://python.org/downloads
    echo  Make sure to tick "Add Python to PATH" during install.
    pause
    exit /b 1
)

:: Run quickstart
python quickstart.py
pause
