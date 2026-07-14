; bx 安装包(Inno Setup)。单文件:bx.exe 已全内嵌 wintun/sing-box/brook。
; 版本由 CI 传入:iscc /DMyAppVersion=<tag> /DSourceExe=<path> packaging\windows\bx-setup.iss
; 不装服务(无 bx:// 链接)——服务由托盘「从剪贴板设置」→ 提权 bx setup 创建(见子项目②)。
; manifest=asInvoker,故装完起的托盘非提权;per-action UAC 在托盘内部弹。

#ifndef MyAppVersion
  #define MyAppVersion "0.0.0"
#endif
#ifndef SourceExe
  #define SourceExe "bx.exe"
#endif

[Setup]
AppId={{45A7EBE8-5107-43C8-9968-187473DA778A}
AppName=bx
AppVersion={#MyAppVersion}
AppPublisher=getbx
DefaultDirName={autopf}\bx
DefaultGroupName=bx
DisableProgramGroupPage=yes
PrivilegesRequired=admin
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
UninstallDisplayIcon={app}\bx.exe
UninstallDisplayName=bx
OutputBaseFilename=bx-setup
Compression=lzma2
SolidCompression=yes
WizardStyle=modern

[Files]
Source: "{#SourceExe}"; DestDir: "{app}"; DestName: "bx.exe"; Flags: ignoreversion

[Icons]
Name: "{group}\bx"; Filename: "{app}\bx.exe"; Parameters: "tray"; Comment: "启动 bx 托盘"
Name: "{group}\卸载 bx"; Filename: "{uninstallexe}"

[Run]
Filename: "{app}\bx.exe"; Parameters: "tray"; Description: "立即启动 bx 托盘"; Flags: postinstall nowait skipifsilent

[UninstallRun]
; 卸载前停+删服务(bx uninstall 已实现);runhidden 免黑框。
Filename: "{app}\bx.exe"; Parameters: "uninstall"; Flags: runhidden; RunOnceId: "bxUninstallService"
