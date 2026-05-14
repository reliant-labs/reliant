; Reliant — NSIS installer hooks (Windows)
;
; Purpose: make the `reliant` CLI available on the user's PATH at install time
; so users do not need to launch the GUI to get the terminal command working.
;
; What this does:
;   1. After install: copy reliant-backend.exe to $INSTDIR\cli\reliant.exe and
;      add $INSTDIR\cli to the *user* PATH (HKCU\Environment\Path).
;   2. Broadcast WM_SETTINGCHANGE so newly-opened terminals pick up the new
;      PATH without a logout/login.
;   3. After uninstall: strip $INSTDIR\cli from PATH and delete the CLI binary.
;
; Why per-user PATH (HKCU) and not per-machine (HKLM):
;   - electron-builder defaults to perMachine: false; HKCU does not require
;     elevation. Matches the no-sudo first-run install path on macOS/Linux.
;
; Implementation notes:
;   - We intentionally avoid third-party NSIS string plugins (StrContains,
;     StrRep) to keep the build dependency-free. PATH manipulation is done
;     with the built-in `${WordFind}` and a small loop.
;   - electron-builder injects this file via NSIS `include` (see
;     electron-builder.common.js `nsis.include`).

!include "LogicLib.nsh"
!include "WordFunc.nsh"
!include "FileFunc.nsh"

!insertmacro WordFind
!insertmacro WordReplace

; ---------------------------------------------------------------------------
; Helper: pick the arch subdirectory containing the embedded backend binary.
; electron-builder ships per-arch backends under:
;   $INSTDIR\resources\server\win32-amd64\reliant-backend.exe
;   $INSTDIR\resources\server\win32-arm64\reliant-backend.exe
; Only one will be present per installer.
; ---------------------------------------------------------------------------
!macro _ReliantPickArchDir OutVar
  ${If} ${FileExists} "$INSTDIR\resources\server\win32-arm64\reliant-backend.exe"
    StrCpy ${OutVar} "win32-arm64"
  ${ElseIf} ${FileExists} "$INSTDIR\resources\server\win32-amd64\reliant-backend.exe"
    StrCpy ${OutVar} "win32-amd64"
  ${Else}
    StrCpy ${OutVar} ""
  ${EndIf}
!macroend

; ---------------------------------------------------------------------------
; customInstall — runs after electron-builder lays down the app files.
; ---------------------------------------------------------------------------
!macro customInstall
  Push $0
  Push $1
  Push $2

  !insertmacro _ReliantPickArchDir $0
  ${If} $0 != ""
    CreateDirectory "$INSTDIR\cli"
    CopyFiles /SILENT "$INSTDIR\resources\server\$0\reliant-backend.exe" "$INSTDIR\cli\reliant.exe"

    ; Add $INSTDIR\cli to the user PATH if not already present.
    ReadRegStr $1 HKCU "Environment" "Path"
    ${If} $1 == ""
      WriteRegExpandStr HKCU "Environment" "Path" "$INSTDIR\cli"
    ${Else}
      ; WordFind returns the matching word index, or "" if not found.
      ${WordFind} "$1" ";" "E+1{$INSTDIR\cli}" $2
      ${If} $2 == ""
        ; Append; preserve any trailing semicolon.
        WriteRegExpandStr HKCU "Environment" "Path" "$1;$INSTDIR\cli"
      ${EndIf}
    ${EndIf}

    ; Broadcast WM_SETTINGCHANGE so new shells see the updated PATH.
    SendMessage ${HWND_BROADCAST} ${WM_WININICHANGE} 0 "STR:Environment" /TIMEOUT=5000
  ${EndIf}

  Pop $2
  Pop $1
  Pop $0
!macroend

; ---------------------------------------------------------------------------
; customUnInstall — strip the CLI from PATH and delete the helper binary.
; ---------------------------------------------------------------------------
!macro customUnInstall
  Push $0
  Push $1

  ReadRegStr $0 HKCU "Environment" "Path"
  ${If} $0 != ""
    ; Try several patterns to handle leading/middle/trailing positions.
    ${WordReplace} "$0" ";$INSTDIR\cli" "" "+" $1
    ${If} $1 == $0
      ${WordReplace} "$0" "$INSTDIR\cli;" "" "+" $1
    ${EndIf}
    ${If} $1 == $0
      ${WordReplace} "$0" "$INSTDIR\cli" "" "+" $1
    ${EndIf}
    WriteRegExpandStr HKCU "Environment" "Path" "$1"
    SendMessage ${HWND_BROADCAST} ${WM_WININICHANGE} 0 "STR:Environment" /TIMEOUT=5000
  ${EndIf}

  Delete "$INSTDIR\cli\reliant.exe"
  RMDir  "$INSTDIR\cli"

  Pop $1
  Pop $0
!macroend
