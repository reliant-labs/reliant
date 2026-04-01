@echo off
REM Reliant CLI - Opens Reliant app with specified directory
REM Usage: reliant [path]

setlocal enabledelayedexpansion

REM Get the directory to open (default to current directory)
set "TARGET_DIR=%~1"
if "%TARGET_DIR%"=="" set "TARGET_DIR=%CD%"

REM Convert to absolute path if relative
if "%TARGET_DIR%"=="." set "TARGET_DIR=%CD%"
for %%I in ("%TARGET_DIR%") do set "TARGET_DIR=%%~fI"

REM Check if directory exists
if not exist "%TARGET_DIR%\" (
    echo Error: Directory '%TARGET_DIR%' does not exist
    exit /b 1
)

REM URL encode the path (basic encoding for common characters)
set "ENCODED_PATH=%TARGET_DIR%"
set "ENCODED_PATH=!ENCODED_PATH: =%%20!"
set "ENCODED_PATH=!ENCODED_PATH:\=/!"

REM Construct the deep link URL and open it
set "DEEP_LINK=reliant://open?path=%ENCODED_PATH%"
start "" "%DEEP_LINK%"
