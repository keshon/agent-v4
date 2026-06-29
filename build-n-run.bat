@echo off
setlocal

set BACKEND=http://localhost:5001
set MODEL=local
set WORKSPACE=.
set MAX_TOKENS=8192
set DEBUG=true

if "%~1"=="" (
    echo Usage: run-agent-debug.bat "task"
    exit /b 1
)

go build -o agent.exe .\cmd\agent\main.go && agent.exe ^
  -backend %BACKEND% ^
  -model %MODEL% ^
  -workspace %WORKSPACE% ^
  -max-tokens %MAX_TOKENS% ^
  -debug ^
  "%~1"

endlocal