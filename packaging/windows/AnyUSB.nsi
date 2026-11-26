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

!define MUI_WELCOMEPAGE_TEXT "Trình cài đặt sẽ chuẩn bị đầy đủ để máy này vừa có thể chia sẻ USB vừa có thể nhận USB từ máy khác:$\r$\n$\r$\n• Dịch vụ nền AnyUSB, công cụ CLI và ứng dụng desktop.$\r$\n• Tải và xác minh chữ ký số cho driver USB/IP (usbipd-win và usbip-win2).$\r$\n• Mở tường lửa Windows cho mạng LAN và Tailscale (TCP 47443, UDP 5353).$\r$\n$\r$\nWindows có thể yêu cầu khởi động lại sau khi cài driver."
!define MUI_FINISHPAGE_TEXT "AnyUSB đã sẵn sàng. Nếu Windows yêu cầu khởi động lại để hoàn tất driver USB/IP, hãy khởi động lại trước khi kết nối USB."
!define MUI_FINISHPAGE_RUN "$INSTDIR\AnyUSB-Desktop.exe"
!define MUI_FINISHPAGE_RUN_TEXT "Mở AnyUSB ngay"
!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_LICENSE "${LICENSE}"
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "Vietnamese"

Function .onInit
  ${IfNot} ${IsNativeAMD64}
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
    StrCpy $R9 "1"
  ${Else}
    StrCpy $R9 "0"
  ${EndIf}
FunctionEnd

Section "AnyUSB"
  nsExec::Exec '"$SYSDIR\taskkill.exe" /IM AnyUSB-Desktop.exe /T /F'
  Pop $0
  ${If} $R9 == "1"
    DetailPrint "Đang dừng dịch vụ AnyUSB để nâng cấp tại chỗ…"
    nsExec::ExecToLog '"$SYSDIR\sc.exe" stop anyusbd'
    Pop $0
    nsExec::ExecToLog '"$WINDIR\Sysnative\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -NonInteractive -Command "(Get-Service -Name anyusbd).WaitForStatus([System.ServiceProcess.ServiceControllerStatus]::Stopped,[TimeSpan]::FromSeconds(15))"'
    Pop $0
    ${If} $0 != "0"
      MessageBox MB_ICONSTOP "Không dừng được dịch vụ AnyUSB để nâng cấp. Hãy đóng tác vụ đang dùng USB rồi thử lại."
      Abort
    ${EndIf}
  ${EndIf}
  SetOutPath "$INSTDIR"
  File /r "${PAYLOAD}\*.*"
  DetailPrint "Đang cài dịch vụ AnyUSB và driver USB/IP…"
  nsExec::ExecToLog '"$WINDIR\Sysnative\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -NonInteractive -ExecutionPolicy Bypass -File "$INSTDIR\install-windows.ps1" -BuildDirectory "$INSTDIR" -DesktopInstaller'
  Pop $0
  ${If} $0 == "3010"
    SetRebootFlag true
  ${ElseIf} $0 != "0"
    ${If} $R9 == "1"
      nsExec::Exec '"$SYSDIR\sc.exe" start anyusbd'
      Pop $1
    ${EndIf}
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
  nsExec::Exec '"$WINDIR\Sysnative\WindowsPowerShell\v1.0\powershell.exe" -NoProfile -NonInteractive -Command Remove-NetFirewallRule -Name AnyUSB-Peer,AnyUSB-LAN-Discovery -ErrorAction SilentlyContinue'
  Pop $0
  Delete "$SMPROGRAMS\AnyUSB.lnk"
  DeleteRegKey HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\AnyUSB"
  RMDir /r /REBOOTOK "$INSTDIR"
SectionEnd
