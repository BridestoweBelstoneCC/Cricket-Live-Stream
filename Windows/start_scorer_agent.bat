@echo off
REM ===========================================================
REM  CricketStream - Scorer Agent
REM  Run this on the SCORING laptop (the one with PCS Pro).
REM  Start it once at the beginning of the day and leave the
REM  window open. It shares the scoreboard file with the
REM  streaming laptop. It does not change anything.
REM ===========================================================
cd /d %~dp0

if exist scorer_agent.py goto run
if exist ..\scorer_agent.py cd ..
if exist scorer_agent.py goto run

echo.
echo   Could not find scorer_agent.py.
echo   Put this file in the same folder as scorer_agent.py and try again.
echo.
pause
exit /b 1

:run
python scorer_agent.py %*
if errorlevel 1 (
  echo.
  echo   The agent stopped with an error. If it says 'python is not
  echo   recognised', install Python from python.org and tick
  echo   "Add Python to PATH" during setup.
  echo.
)
pause
