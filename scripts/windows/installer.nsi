; installer.nsi — ccfly Windows 安装器(NSIS 3,macOS/Linux 上用 makensis 交叉构建)。
;
; 安装器只做「铺文件 + PATH + 快捷方式」;真正的服务注册(计划任务、二进制
; 自复制到 ~/.ccfly/bin、配对)全部由 `ccfly install` 自己完成 —— 安装器在
; 完成页提供一键入口,升级 = 重跑安装器 + 重跑 `ccfly install`。
;
; 构建(见 build-windows-installer.sh,不要手工调用):
;   makensis -DVERSION=x.y.z -DBINDIR=<npm/ccfly-win32-x64/bin 绝对路径> \
;            -DOUTFILE=<输出 exe 绝对路径> -DESTIMATED_SIZE_KB=<KB> installer.nsi
;
; 设计要点:
;   - **管理员安装**(RequestExecutionLevel admin,启动即一次 UAC):SNI arm 用 hosts 模式,
;     写 %SystemRoot%\System32\drivers\etc\hosts 必须提权 token;且 HighestAvailable 计划任务
;     只有从提权上下文注册,运行时才真正拿到高 token。完成页/快捷方式跑的 `ccfly install`
;     继承安装器的提权,一并解决。
;   - 布局仍是 per-user:装到 $LOCALAPPDATA\Programs\ccfly、注册表只碰 HKCU —— 服务模型
;     (~/.ccfly 身份、InteractiveToken 任务)本就按登录用户走。UAC 同用户提权时 $LOCALAPPDATA/
;     HKCU 不变;**标准账号不支持**(UAC 会切到别的管理员,profile 全部错位)。
;   - 已知取舍:HighestAvailable 任务跑的是用户可写目录里的二进制(~/.ccfly/bin)。
;   - PATH 改动走 addpath.ps1/delpath.ps1(nsExec 调 powershell),绕开 NSIS
;     1024 字符字符串上限对超长用户 PATH 的截断破坏。
;   - 卸载保守:删程序目录、快捷方式、PATH 项、计划任务、hosts 托管块(经提权的
;     `ccfly uninstall`)、~/.ccfly 下的服务副本与包装脚本;保留 ~/.ccfly 的设备
;     身份/日志(重装免重新配对)。

Unicode true
SetCompressor /SOLID lzma

!include "MUI2.nsh"
!include "WinMessages.nsh"

!ifndef VERSION
  !define VERSION "0.0.0"
!endif
!ifndef BINDIR
  !define BINDIR "../../npm/ccfly-win32-x64/bin"
!endif
!ifndef OUTFILE
  !define OUTFILE "ccfly-setup-${VERSION}-x64.exe"
!endif
!ifndef ESTIMATED_SIZE_KB
  !define ESTIMATED_SIZE_KB 22000
!endif
!ifndef HOST
  ; 配对目标 host,烤进完成页一键运行与开始菜单快捷方式(免去终端里交互选 host)
  !define HOST "cc.hn"
!endif

!define UNINST_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\ccfly"

Name "ccfly ${VERSION}"
OutFile "${OUTFILE}"
InstallDir "$LOCALAPPDATA\Programs\ccfly"
InstallDirRegKey HKCU "Software\ccfly" "InstallDir"
RequestExecutionLevel admin

VIProductVersion "${VERSION}.0"
VIAddVersionKey /LANG=2052 "ProductName" "ccfly"
VIAddVersionKey /LANG=2052 "ProductVersion" "${VERSION}"
VIAddVersionKey /LANG=2052 "FileVersion" "${VERSION}"
VIAddVersionKey /LANG=2052 "FileDescription" "ccfly — Claude Code 设备网关"
VIAddVersionKey /LANG=2052 "LegalCopyright" "MIT License"

; --- MUI 页面 ---------------------------------------------------------------
!define MUI_ICON "ccfly.ico"
!define MUI_UNICON "ccfly.ico"

!define MUI_WELCOMEPAGE_TEXT "即将安装 ccfly ${VERSION}(Claude Code 设备网关)。$\r$\n$\r$\n安装内容:ccfly.exe 与捆绑的 tmux.exe(psmux),并加入用户 PATH。$\r$\n$\r$\n安装完成后可一键运行「ccfly install」完成配对并注册开机自启的后台服务。"

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES

!define MUI_FINISHPAGE_RUN
!define MUI_FINISHPAGE_RUN_TEXT "现在配对并注册后台服务(运行 ccfly install ${HOST})"
!define MUI_FINISHPAGE_RUN_FUNCTION RunPairing
!define MUI_FINISHPAGE_TEXT "ccfly 已安装到本机。$\r$\n$\r$\n勾选下方选项将打开终端运行「ccfly install ${HOST}」:浏览器会自动打开配对页面,登录后点「批准」即可,之后 ccfly 会注册为登录自启、掉线自愈的后台任务。$\r$\n$\r$\n升级提示:重装新版本后需再跑一次「ccfly install」(开始菜单有快捷方式),后台服务才会切换到新二进制。"
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "SimpChinese"
!insertmacro MUI_LANGUAGE "English"

; --- 安装 -------------------------------------------------------------------
Section "ccfly" SecMain
  SectionIn RO
  SetOutPath "$INSTDIR"

  File "${BINDIR}/ccfly.exe"
  File "${BINDIR}/tmux.exe"
  File "addpath.ps1"
  File "delpath.ps1"

  WriteRegStr HKCU "Software\ccfly" "InstallDir" "$INSTDIR"
  WriteUninstaller "$INSTDIR\uninstall.exe"

  ; 用户 PATH(经 PowerShell,幂等)
  nsExec::ExecToLog 'powershell.exe -NoProfile -ExecutionPolicy Bypass -File "$INSTDIR\addpath.ps1" -Dir "$INSTDIR"'
  Pop $0
  SendMessage ${HWND_BROADCAST} ${WM_WININICHANGE} 0 "STR:Environment" /TIMEOUT=5000

  ; 开始菜单
  CreateDirectory "$SMPROGRAMS\ccfly"
  CreateShortcut "$SMPROGRAMS\ccfly\ccfly 配对安装.lnk" "$SYSDIR\cmd.exe" '/k ""$INSTDIR\ccfly.exe" install ${HOST}"' "$INSTDIR\ccfly.exe" 0
  ; 给配对快捷方式打「以管理员身份运行」标志(.lnk 头 LinkFlags 偏移 21 的 0x20 位;
  ; 免 ShellLink 插件):`ccfly install` 在 Windows 上强制要求管理员(写 hosts + 注册
  ; HighestAvailable 任务),点快捷方式直接走 UAC,而不是进去才报「请以管理员重跑」。
  FileOpen $0 "$SMPROGRAMS\ccfly\ccfly 配对安装.lnk" a
  FileSeek $0 21 SET
  FileReadByte $0 $1
  IntOp $1 $1 | 32
  FileSeek $0 21 SET
  FileWriteByte $0 $1
  FileClose $0
  CreateShortcut "$SMPROGRAMS\ccfly\卸载 ccfly.lnk" "$INSTDIR\uninstall.exe"

  ; 「应用和功能」卸载项(HKCU,与 per-user 安装同层)
  WriteRegStr HKCU "${UNINST_KEY}" "DisplayName" "ccfly"
  WriteRegStr HKCU "${UNINST_KEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${UNINST_KEY}" "Publisher" "ccfly"
  WriteRegStr HKCU "${UNINST_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINST_KEY}" "DisplayIcon" "$INSTDIR\ccfly.exe"
  WriteRegStr HKCU "${UNINST_KEY}" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegStr HKCU "${UNINST_KEY}" "QuietUninstallString" '"$INSTDIR\uninstall.exe" /S'
  WriteRegDWORD HKCU "${UNINST_KEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINST_KEY}" "NoRepair" 1
  WriteRegDWORD HKCU "${UNINST_KEY}" "EstimatedSize" ${ESTIMATED_SIZE_KB}
SectionEnd

Function RunPairing
  ; 安装器已提权(RequestExecutionLevel admin),ExecShell 起的 cmd 继承高 token:
  ; `ccfly install` 在提权上下文里注册 HighestAvailable 任务(运行时才真拿到管理员,
  ; SNI arm 写 hosts 靠它),Windows 上 `ccfly install` 自身也强制要求管理员。
  ; cmd /k ""exe" args" —— 首尾引号被 cmd 剥掉(>2 引号规则),窗口保留供交互配对。
  ExecShell "open" "$SYSDIR\cmd.exe" '/k ""$INSTDIR\ccfly.exe" install ${HOST}"' SW_SHOWNORMAL
FunctionEnd

; --- 卸载 -------------------------------------------------------------------
Section "Uninstall"
  ; 1) 摘除计划任务 + 清 hosts 托管块(schtasks /End + /Delete + restoreResolver,由
  ;    ccfly 自己完成;卸载器已提权,hosts 才写得动 —— 不清会把 api.anthropic.com 等
  ;    钉死在 127.0.0.1 上没人接,整机 Claude 全断)
  nsExec::ExecToLog '"$INSTDIR\ccfly.exe" uninstall'
  Pop $0
  ; 2) 兜底杀掉残余 ccfly 进程(不动 tmux.exe —— 里面可能跑着用户的 Claude 会话)
  nsExec::ExecToLog 'taskkill.exe /F /IM ccfly.exe'
  Pop $0

  ; 3) 清 `ccfly install` 落在 ~/.ccfly 的服务副本与包装脚本;
  ;    保留设备身份/日志/配置,重装无需重新配对
  Delete "$PROFILE\.ccfly\bin\ccfly.exe"
  Delete "$PROFILE\.ccfly\bin\tmux.exe"
  Delete "$PROFILE\.ccfly\bin\*.old*"
  Delete "$PROFILE\.ccfly\ccfly-task.cmd"
  Delete "$PROFILE\.ccfly\ccfly-task.vbs"
  RMDir "$PROFILE\.ccfly\bin"

  ; 4) 摘 PATH(要在删 delpath.ps1 之前跑)
  nsExec::ExecToLog 'powershell.exe -NoProfile -ExecutionPolicy Bypass -File "$INSTDIR\delpath.ps1" -Dir "$INSTDIR"'
  Pop $0
  SendMessage ${HWND_BROADCAST} ${WM_WININICHANGE} 0 "STR:Environment" /TIMEOUT=5000

  ; 5) 程序目录 + 快捷方式 + 注册表
  Delete "$INSTDIR\ccfly.exe"
  Delete "$INSTDIR\tmux.exe"
  Delete "$INSTDIR\addpath.ps1"
  Delete "$INSTDIR\delpath.ps1"
  Delete "$INSTDIR\uninstall.exe"
  RMDir "$INSTDIR"

  Delete "$SMPROGRAMS\ccfly\ccfly 配对安装.lnk"
  Delete "$SMPROGRAMS\ccfly\卸载 ccfly.lnk"
  RMDir "$SMPROGRAMS\ccfly"

  DeleteRegKey HKCU "${UNINST_KEY}"
  DeleteRegKey HKCU "Software\ccfly"
SectionEnd
