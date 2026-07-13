# Windows 打包分发 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 给 `bx.exe` 嵌 UAC-aware manifest + 图标 + 版本信息,产出 Inno Setup 安装包 `bx-setup.exe`,并把 Windows 接入 release 产物。

**Architecture:** 三部分独立可验——(A) `go-winres` 生成 `.syso` 把 manifest/icon/version 链入 windows 构建;(B) `packaging/windows/bx-setup.iss` 薄安装包;(C) `release.yml`/`ci.yml` 接入 windows 交叉编译 + windows runner 跑 `iscc`。这是打包/配置子项目,单元测试面很小:唯一 dev 可自动验的是「`.syso` 不破 windows 构建」;`.iss`/release 全流程靠 CI + 真机。

**Tech Stack:** Go 1.26、`github.com/tc-hib/go-winres@v0.3.3`(纯 Go,Linux 可跑)、Inno Setup(`iscc`,windows-only)、GitHub Actions。

## Global Constraints

- Go 1.26;仓库 **gofumpt** 格式化。
- **manifest 恒 `asInvoker`,绝不 `requireAdministrator`/`highestAvailable`**——保子项目②的非提权托盘 + per-action UAC。这是硬不变量。
- **不做 code signing**(无证书);产物无签名,文档告知 SmartScreen「仍要运行」。
- **安装包 amd64-only**;便携 `bx.exe` 出 amd64+arm64。
- `.syso` 提交进仓库根,`go build` 自动链入 windows 构建;真相源是文本 `winres/winres.json`(binary `.syso` review 不了)。
- 固定安装包 AppId GUID:`{45A7EBE8-5107-43C8-9968-187473DA778A}`(永不变,否则升级/卸载错乱)。
- 验证命令:`go build ./... && go vet ./... && go test ./...`;windows 交叉编译 `GOOS=windows GOARCH=amd64/arm64 go build -o /dev/null .`;linux/darwin 不受 `.syso` 影响。
- 提交:中文 conventional commits,结尾 `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`。默认分支直接提交。

## File Structure

- `winres/winres.json` — 资源描述(manifest/icon/version)。**真相源**。
- `winres/icon.png`(256²)+ `winres/icon16.png`(32²)— 应用图标源(绿盾 bx 标,go-winres 生成 icon group)。
- `winres/README.md` — 重生成 `.syso` 的方式 + CI 版本覆盖说明。
- `generate_windows.go`(repo 根,`package main`)— 承载 `//go:generate` 指令。
- `rsrc_windows_amd64.syso` / `rsrc_windows_arm64.syso` — 生成并提交(Go 链接器按 GOOS/GOARCH 自动链)。
- `packaging/windows/bx-setup.iss` — Inno Setup 脚本。
- `.github/workflows/release.yml`(改)— 加 windows 交叉编译 + windows job 出安装包。
- `.github/workflows/ci.yml`(改)— 加 windows 交叉编译守卫(`.syso` 不破构建)。

**注**:`winres/{winres.json,icon.png,icon16.png}` 与两个 `.syso` 已在规划阶段生成并验证(windows amd64/arm64 build 链入成功、`go-winres extract` 证资源已嵌、linux/darwin 不受影响),当前在工作树未提交。Task 1 负责补齐 `generate_windows.go`+`README` 并把它们一并提交。

---

### Task 1: Part A — exe 资源(manifest + 图标 + 版本)

**Files:**
- Present in worktree (commit them): `winres/winres.json`, `winres/icon.png`, `winres/icon16.png`, `rsrc_windows_amd64.syso`, `rsrc_windows_arm64.syso`
- Create: `generate_windows.go`, `winres/README.md`

**Interfaces:**
- Produces: 一个带 `asInvoker` manifest + 图标 + 版本信息的 windows `bx.exe`;`go generate ./...` 可重生成 `.syso`。
- Consumes: 无(纯打包资源,不碰任何 Go 逻辑)。

**背景**:`winres/winres.json` 已是下述验证过的内容(manifest `execution-level: "as invoker"`、`dpi-awareness: "per monitor v2"`、`long-path-aware: true`、icon group APP、version info)。若工作树缺失,用本任务 Step 2 的命令重建。

- [ ] **Step 1: 确认 winres 资产在位**

Run: `ls -la winres/winres.json winres/icon.png winres/icon16.png rsrc_windows_amd64.syso rsrc_windows_arm64.syso`
Expected: 五个文件都在。若 `.syso` 缺失,跑 Step 2 重生成;若 `winres/` 缺失,见下方「重建 winres.json」附录。

- [ ] **Step 2: 重生成 `.syso`(确认可复现)**

Run: `go run github.com/tc-hib/go-winres@v0.3.3 make --in winres/winres.json --arch amd64,arm64 --out rsrc`
Expected: 生成 `rsrc_windows_amd64.syso` + `rsrc_windows_arm64.syso`(各 ~5.5KB),无报错。

- [ ] **Step 3: 验证资源链入 windows 构建 + 不连累其他平台**

Run:
```bash
GOOS=windows GOARCH=amd64 go build -o /dev/null . && \
GOOS=windows GOARCH=arm64 go build -o /dev/null . && \
go build ./... && GOOS=darwin GOARCH=arm64 go build -o /dev/null ./... && echo ALL_OK
```
Expected: `ALL_OK`(windows 两 arch 带 `.syso` 构建成功;linux/darwin 不受影响)。

- [ ] **Step 4: 写 `generate_windows.go`(承载 go:generate 指令)**

```go
package main

// Windows exe 资源(manifest / 图标 / 版本信息)由 go-winres 从 winres/winres.json
// 生成为 rsrc_windows_{amd64,arm64}.syso,Go 链接器按 GOOS/GOARCH 自动链入 windows 构建。
// 换图标/版本后:`go generate ./...` 重生成 .syso 并提交。真相源是文本 winres/winres.json。
//
//go:generate go run github.com/tc-hib/go-winres@v0.3.3 make --in winres/winres.json --arch amd64,arm64 --out rsrc
```

（无 import、无代码——只承载指令。文件名 `_windows.go` 后缀不影响 `go generate`(它扫描所有 .go),但语义上归拢 windows 资源。若担心该文件在非 windows 构建被编译,注意它只有 package 声明 + 注释,任何平台都能编译,无副作用。）

- [ ] **Step 5: 写 `winres/README.md`**

```markdown
# winres — Windows exe 资源

`bx.exe` 的 manifest(UAC / DPI / long-path)、应用图标、版本信息。

- **真相源**:`winres.json`(文本,可 review)。
- **图标源**:`icon.png`(256²)+ `icon16.png`(32²),go-winres 合成 icon group。
- **产物**:`rsrc_windows_amd64.syso` / `rsrc_windows_arm64.syso`(提交进仓库根),
  Go 链接器按 GOOS/GOARCH 自动链入 windows 构建。

## 重生成

改了 winres.json 或图标后:

    go generate ./...        # 等价 go-winres make --in winres/winres.json --arch amd64,arm64 --out rsrc

然后提交新的 `.syso`。

## 不变量

- manifest `execution-level` 恒 `as invoker`——**绝不** requireAdministrator/highestAvailable。
  bx 托盘非提权常驻、改动系统的动作各自 per-action UAC(见子项目②);强制提权会推翻该设计。

## 版本

`winres.json` 里 version 占位 `0.0.0.0`;release 时 CI 用
`go-winres make … --product-version git-tag --file-version git-tag` 覆盖成 tag 版本
(与 `-ldflags -X internal/version.Version` 一致)。
```

- [ ] **Step 6: 全平台构建 + 测试**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: 全绿(资源不影响任何 Go 逻辑/测试)。

- [ ] **Step 7: 提交**

```bash
git add winres/ rsrc_windows_amd64.syso rsrc_windows_arm64.syso generate_windows.go
git commit -m "feat(windows): exe 嵌 asInvoker manifest + 图标 + 版本(go-winres .syso)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

**附录:重建 winres.json**(仅当工作树缺失时)——`go run github.com/tc-hib/go-winres@v0.3.3 init` 生成模板后,把 `execution-level` 改 `"as invoker"`、`dpi-awareness` 改 `"per monitor v2"`、`long-path-aware` 改 `true`、`use-common-controls-v6` 改 `true`、`identity.name` 改 `"getbx.bx"`、version info 填 CompanyName=`getbx`/ProductName=`bx`/FileDescription=`bx transparent proxy`/InternalName=`bx`/OriginalFilename=`bx.exe`;图标 PNG 用绿盾(见规划记录)。

---

### Task 2: Part B — Inno Setup 安装包

**Files:**
- Create: `packaging/windows/bx-setup.iss`

**Interfaces:**
- Consumes: 构建产物 `bx.exe`(Task 1 带资源;Task 3 CI 交叉编译)。
- Produces: `bx-setup.exe`(Task 3 CI 用 `iscc` 编)。

**注**:`.iss` 无法在 Linux 编译(`iscc` 是 windows 工具),本任务交付**文件本身**;语法/行为由 Task 3 CI(`iscc`)+ 真机验收兜。写得尽量薄(单文件安装,逻辑最少)。

- [ ] **Step 1: 写 `packaging/windows/bx-setup.iss`**

```ini
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
```

- [ ] **Step 2: 结构自检**

Run: `grep -n 'AppId=\|PrivilegesRequired=admin\|Parameters: "tray"\|Parameters: "uninstall"\|OutputBaseFilename=bx-setup' packaging/windows/bx-setup.iss`
Expected: 五处都命中(固定 GUID、安装器自提权、开始菜单+装完起托盘、卸载删服务、产物名)。

- [ ] **Step 3: 提交**

```bash
git add packaging/windows/bx-setup.iss
git commit -m "feat(windows): Inno Setup 安装包脚本 bx-setup.iss(装 exe+开始菜单+卸载删服务)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Part C — release.yml / ci.yml 接入 Windows

**Files:**
- Modify: `.github/workflows/release.yml`
- Modify: `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: Task 1 `.syso`(交叉编译自动链)、Task 2 `bx-setup.iss`。
- Produces: release 附 `bx_windows_{amd64,arm64}.zip` + `bx-setup.exe`;CI 加 windows 构建守卫。

**注**:release 全流程只有打 tag 才真跑;本任务靠 yaml 结构正确 + 逻辑 review,真验在下次 release + 真机。

- [ ] **Step 1: 改 `release.yml` 的 build job 加 windows 交叉编译 + zip**

在现有 `for os in linux darwin` 循环**之后**(windows 要 zip 不 tar,且要先 go-winres 覆盖版本),加:

```yaml
      - name: Build Windows artifacts
        run: |
          set -euo pipefail
          version="${{ github.event_name == 'workflow_dispatch' && inputs.tag || github.ref_name }}"
          commit="${GITHUB_SHA::12}"
          date="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
          ldflags="-s -w -X github.com/getbx/bx/internal/version.Version=$version -X github.com/getbx/bx/internal/version.Commit=$commit -X github.com/getbx/bx/internal/version.Date=$date"
          # PE VersionInfo 覆盖成 tag 版本(与 ldflags 版本一致),重生成 .syso 后再编
          go run github.com/tc-hib/go-winres@v0.3.3 make --in winres/winres.json --arch amd64,arm64 --out rsrc \
            --product-version "$version" --file-version "$version"
          for arch in amd64 arm64; do
            CGO_ENABLED=0 GOOS=windows GOARCH="$arch" go build -trimpath -ldflags="$ldflags" -o "dist/bx.exe" .
            (cd dist && zip -q "bx_windows_${arch}.zip" bx.exe && rm bx.exe)
          done
          # 留一份 amd64 exe 给下游 installer job
          CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="$ldflags" -o "dist/bx-amd64.exe" .

      - name: Upload windows exe for installer job
        uses: actions/upload-artifact@v4
        with:
          name: bx-windows-amd64-exe
          path: dist/bx-amd64.exe
```

并把最后 `sha256sum *.tar.gz > SHA256SUMS` 改为覆盖 zip:
```yaml
          cd dist
          sha256sum *.tar.gz *.zip > SHA256SUMS
```
（`bx-amd64.exe` 是给 installer job 的中转,**不进 SHA256SUMS、不进 release**;仅 `*.zip`/`*.tar.gz`。）

- [ ] **Step 2: 加 installer job(windows runner 跑 iscc)**

在 `release.yml` 加第二个 job:

```yaml
  installer:
    needs: build
    runs-on: windows-latest
    permissions:
      contents: write
    steps:
      - uses: actions/checkout@v5

      - name: Download windows exe
        uses: actions/download-artifact@v4
        with:
          name: bx-windows-amd64-exe
          path: dist

      - name: Install Inno Setup
        run: choco install innosetup --no-progress -y

      - name: Build installer
        shell: pwsh
        run: |
          $version = if ("${{ github.event_name }}" -eq "workflow_dispatch") { "${{ inputs.tag }}" } else { "${{ github.ref_name }}" }
          $v = $version.TrimStart("v")
          & iscc "/DMyAppVersion=$v" "/DSourceExe=dist\bx-amd64.exe" "packaging\windows\bx-setup.iss"
          # Inno 输出到 Output\bx-setup.exe
          Get-ChildItem Output

      - name: Upload installer to release
        uses: softprops/action-gh-release@v2
        with:
          tag_name: ${{ github.event_name == 'workflow_dispatch' && inputs.tag || github.ref_name }}
          files: Output/bx-setup.exe
```

（注:`iscc` 默认输出到 `.iss` 同级或 `Output\`;`.iss` 未设 `OutputDir`,默认 `Output\`。`ArchitecturesInstallIn64BitMode` 已限 x64。version 去 `v` 前缀喂 `MyAppVersion`。）

- [ ] **Step 3: 现有 build job 的 upload 保持**(release 附 zip)

确认 build job 末尾的 `softprops/action-gh-release` 的 `files:` 覆盖 `dist/*.zip`:
```yaml
          files: |
            dist/*.tar.gz
            dist/*.zip
            dist/SHA256SUMS
```

- [ ] **Step 4: 改 `ci.yml` 加 windows 交叉编译守卫**

在 `ci.yml` 的构建/测试步骤里(或新 step)加,确保 `.syso` 与 windows 代码不破构建:

```yaml
      - name: Windows cross-compile guard
        run: |
          GOOS=windows GOARCH=amd64 go build -o /dev/null .
          GOOS=windows GOARCH=arm64 go build -o /dev/null .
```

（若 `ci.yml` 已有跨平台矩阵含 windows,则此步冗余、可跳;实现时先读 `ci.yml` 现状,只补缺的。）

- [ ] **Step 5: yaml 结构自检**

Run:
```bash
python3 -c "import yaml,sys; [yaml.safe_load(open(f)) for f in ['.github/workflows/release.yml','.github/workflows/ci.yml']]; print('YAML_OK')"
grep -n 'runs-on: windows-latest\|choco install innosetup\|iscc\|bx_windows_\|bx-setup.exe' .github/workflows/release.yml
```
Expected: `YAML_OK`;windows job / iscc / zip 产物 / 安装包上传都命中。

- [ ] **Step 6: 提交**

```bash
git add .github/workflows/release.yml .github/workflows/ci.yml
git commit -m "ci(windows): release 出 bx_windows_*.zip + bx-setup.exe;ci 加 windows 交叉编译守卫

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: 文档 — README 加 Windows 安装 + SmartScreen 提示

**Files:**
- Modify: `README.md`

**Interfaces:**
- Consumes: Task 2/3 产物名(`bx-setup.exe`、`bx_windows_*.zip`)。

- [ ] **Step 1: 读 README 现状找插入点**

Run: `grep -n '^#\|安装\|install\|Windows\|下载' README.md | head -30`
Expected: 定位到安装/下载章节(在其中或其后加 Windows 小节)。

- [ ] **Step 2: 加 Windows 安装小节**

在合适位置插入(措辞随 README 现有风格微调):

```markdown
### Windows

从 [Releases](https://github.com/getbx/bx/releases) 下载:

- **安装包(推荐小白)**:`bx-setup.exe` —— 双击安装到 `Program Files`、开始菜单、可从「添加/删除程序」卸载,装完自动起托盘。
- **便携版**:`bx_windows_amd64.zip` / `bx_windows_arm64.zip` —— 解压即用的单个 `bx.exe`(CLI + 服务 + 托盘 + 全内嵌 wintun/sing-box/brook)。

> ⚠️ **首次运行 SmartScreen 提示**:产物暂未做代码签名,Windows 会弹「Windows 已保护你的电脑 / 未知发行者」。点 **更多信息 → 仍要运行** 即可。(有证书后会补签名,提示消失。)

装好后:双击托盘图标 → 复制你的 `bx://` 链接 → 「从剪贴板设置」→ UAC 提权 → 「连接」,整机接管。
```

- [ ] **Step 3: 提交**

```bash
git add README.md
git commit -m "docs(readme): Windows 安装说明(bx-setup.exe / 便携版)+ SmartScreen 提示

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: 真机验收(030-SJWJ-GSR-B,与子项目②合并)

**非 TDD——真机 e2e;需人在 Windows 机器旁(GUI + UAC 交互)。失败回对应 Task 修。**

- [ ] **Step 1: 本地交叉编译带资源的 bx.exe**(模拟 release 产物,验图标/版本/manifest)

Run: `GOOS=windows GOARCH=amd64 go build -o bx.exe .`(带 `.syso`)

- [ ] **Step 2: 真机装 + 验(安装包路径)**

在真机(或 CI 产出的 `bx-setup.exe`):双击 → SmartScreen「仍要运行」→ 安装 → 开始菜单出「bx」(图标带 bx.ico)→ `bx.exe` Properties 显版本/公司 → 托盘图标出现(非提权,无黑框)→ 复制 reality `bx://` → 托盘「从剪贴板设置」(UAC)→「连接」(UAC)→ 出口==VPS → 「断开」→ 从「添加/删除程序」卸载 → 服务/文件清干净、无残留。

- [ ] **Step 3: 便携版验**:解压 `bx_windows_amd64.zip` 的单个 `bx.exe` 到空目录 → 双击起托盘 → 同上连通。

---

## Self-Review

- **Spec coverage**:Part A(manifest asInvoker + 图标 + 版本 via go-winres .syso)→ Task 1;Part B(Inno .iss:装 exe/开始菜单/添加删除程序/装完起托盘/卸载删服务)→ Task 2;Part C(release 出 zip + 安装包、ci 守卫)→ Task 3;SmartScreen 文档 → Task 4;真机验收 → Task 5;不签名/amd64-only installer/固定 GUID → Global Constraints。覆盖齐。
- **Placeholder scan**:Task 1 内容已在规划阶段实跑验证(非猜测);GUID 固定实值;`.iss`/yaml 为完整可用文件;唯一「待实现期定」是 `ci.yml` 现状(Task 3 Step 4 明确「先读现状只补缺的」)——非占位,是防重复。无 TBD/TODO。
- **Type consistency**:无跨任务 Go 符号(纯打包);文件名/产物名一致——`rsrc_windows_{amd64,arm64}.syso`(Task 1 生成、Task 3 交叉编译链入)、`bx-setup.iss`+GUID `45A7EBE8-…`(Task 2 定义、Task 3 `iscc` 用)、`bx_windows_{amd64,arm64}.zip`/`bx-setup.exe`(Task 3 产、Task 4 README 引、Task 5 验)全对齐。
