Unicode true
!include "MUI2.nsh"
!include "x64.nsh"
!include "LogicLib.nsh"

Name "AnyUSB"
OutFile "${OUTPUT}"
InstallDir "$PROGRAMFILES64\AnyUSB"
RequestExecutionLevel admin
SetCompressor zlib
ShowInstDetails show
ShowUninstDetails show
BrandingText "AnyUSB"

!define MUI_WELCOMEPAGE_TEXT "AnyUSB sẽ cài dịch vụ, driver USB/IP và lối mở trong Start Menu. Windows có thể yêu cầu khởi động lại sau khi cài driver.$\r$\n$\r$\nBấm Tiếp theo để xem và bắt đầu cài."
!define MUI_FINISHPAGE_TEXT "Mở AnyUSB từ Start Menu. Nếu Windows yêu cầu khởi động lại để hoàn tất driver, hãy khởi động lại trước khi kết nối USB."
!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_LICENSE "${LICENSE}"
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "Vietnamese"

Function .onInit
  ${IfNot} ${RunningX64}
    MessageBox MB_ICONSTOP "Bộ cài này cần Windows x64."
    Abort
  ${EndIf}
  SetRegView 64
  StrCpy $INSTDIR "$PROGRAMFILES64\AnyUSB"
  SetShellVarContext all
  System::Call 'kernel32::GetFileAttributesW(w "$INSTDIR")i.r0'
  ${If} $0 != -1
    IntOp $0 $0 & 0x400
    ${If} $0 != 0
      MessageBox MB_ICONSTOP "Thư mục cài AnyUSB không hợp lệ."
      Abort
    ${EndIf}
  ${EndIf}
  nsExec::Exec '"$SYSDIR\sc.exe" query anyusbd'
  Pop $0
  ${If} $0 == "0"
    MessageBox MB_ICONSTOP "AnyUSB đã được cài. Hãy đóng các tác vụ và gỡ bản cũ trong Settings trước khi cài bản này."
    Abort
  ${EndIf}
FunctionEnd

Section "AnyUSB"
  SetOutPath "$INSTDIR"
  File /r "${PAYLOAD}\*.*"
  DetailPrint "Đang cài dịch vụ AnyUSB và driver USB/IP…"
  nsExec::ExecToLog '"$SYSDIR\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "$INSTDIR\install-windows.ps1" -BuildDirectory "$INSTDIR" -DesktopInstaller -Python "$INSTDIR\runtime\python.exe"'
  Pop $0
  ${If} $0 == "3010"
    SetRebootFlag true
  ${ElseIf} $0 != "0"
    MessageBox MB_ICONSTOP "Không cài được dịch vụ hoặc driver. Xem phần chi tiết trong bộ cài."
    Abort
  ${EndIf}
  CreateShortCut "$SMPROGRAMS\AnyUSB.lnk" "$INSTDIR\AnyUSB-Desktop.exe" "" "$INSTDIR\anyusb.ico"
  WriteUninstaller "$INSTDIR\Uninstall.exe"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\AnyUSB" "DisplayName" "AnyUSB"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\AnyUSB" "DisplayVersion" "${VERSION}"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\AnyUSB" "Publisher" "AnyUSB"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\AnyUSB" "UninstallString" '"$INSTDIR\Uninstall.exe"'
SectionEnd

Section "Uninstall"
  SetRegView 64
  SetShellVarContext all
  ${If} $INSTDIR != "$PROGRAMFILES64\AnyUSB"
    Abort
  ${EndIf}
  nsExec::Exec '"$SYSDIR\sc.exe" stop anyusbd'
  Pop $0
  nsExec::Exec '"$SYSDIR\sc.exe" delete anyusbd'
  Pop $0
  nsExec::Exec '"$SYSDIR\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -NonInteractive -Command Remove-NetFirewallRule -Name AnyUSB-Peer,AnyUSB-LAN-Discovery -ErrorAction SilentlyContinue'
  Pop $0
  Delete "$SMPROGRAMS\AnyUSB.lnk"
  DeleteRegKey HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\AnyUSB"
  RMDir /r /REBOOTOK "$INSTDIR"
SectionEnd
