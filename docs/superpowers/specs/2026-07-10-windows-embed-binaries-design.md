# 子项目①:内嵌 Windows 二进制(设计)

> 属「Windows 消费级分发」整体方案(见 `2026-07-09-windows-consumer-distribution-design.md`)
> 的第一块地基。目标:让 `bx.exe` **自包含**——内嵌 wintun.dll + sing-box + brook,零下载、
> 零手动随行文件,顺带彻底消掉企业 TLS MITM 挡 github 下载的坑。对齐 linux/darwin 已有的
> 内嵌做法(`internal/embedded` 条件 embed + `provision.Ensure*` 释放)。

## 1. 背景与约束

- linux/darwin 的 bx 已内嵌 brook + sing-box(自建静态,`with_utls,with_quic`,`CGO_ENABLED=0`),
  经 `internal/embedded/embedded_*.go` 按 GOOS/GOARCH 条件 embed,`provision.Ensure*` 首次释放到
  DataDir。windows/其他 arch 目前 nil 兜底 → 下载。本子项目给 windows amd64/arm64 补上内嵌。
- **wintun.dll 加载器约束(关键)**:bx 经 wireguard-go 用 `golang.zx2c4.com/wintun`,其
  `dll.go` 以 `windows.LoadLibraryEx("wintun.dll", 0, LOAD_LIBRARY_SEARCH_APPLICATION_DIR |
  LOAD_LIBRARY_SEARCH_SYSTEM32)` 加载——**只搜 exe 所在目录 + System32**,不认 CWD / DataDir /
  SetDllDirectory / 内存加载。故内嵌的 wintun.dll **必须释放到 exe 同目录**才能被加载。
  sing-box/brook 是**子进程**、按绝对路径 exec,无此约束,照常释放到 DataDir。

## 2. 内嵌矩阵

覆盖 **windows/amd64 + windows/arm64**(每构建只嵌匹配 arch,同现有 linux/darwin):

| 资产 | 来源 | 大小 | 释放到 |
|---|---|---|---|
| `wintun.dll` | wintun.net 0.14.1 官方签名 DLL(amd64/arm64) | ~427KB | **exe 同目录**(加载器约束) |
| `sing-box` | CI 自建静态(同 linux:`CGO_ENABLED=0 -tags with_utls,with_quic`) | ~28MB | DataDir(`provision.EnsureSingbox`) |
| `brook` | 官方 `brook_windows_{amd64,arm64}.exe` | ~30MB | DataDir(`provision.EnsureBrook`) |

`bx.exe` 每构建 ~80MB(同 linux 内嵌量级)。非 windows/amd64/arm64 arch 仍 nil 兜底→下载。

## 3. wintun.dll:运行时释放到 exe 同目录

新增 `provision.EnsureWintun(exeDir string, wintunBytes []byte, version string) error`(纯 windows
或平台无关+windows 调用):
- 目标 = `filepath.Join(exeDir, "wintun.dll")`,`exeDir = filepath.Dir(os.Executable())`。
- 版本键缓存:`exeDir/.wintun-version` 与 `embedCacheKey(version, bytes)` 一致且目标存在 → 复用;
  否则原子写出(同目录临时文件 + rename,0644)。
- 调用点 = `platform_windows.OpenTUN` **首行**(windows-local,不把 exeDir/embedded 穿过 run.go
  的平台无关核心;`OpenTUN` 里直接 `embedded.Wintun()` + `os.Executable()` 拿 exeDir)。
  失败返回清晰错误(exe 目录不可写等罕见情况),阻断 OpenTUN(否则 CreateTUN 必因缺 dll 失败)。
- **幂等**:多次启动只在缺失/版本变时写。

**取代**:删 `internal/install/sidefiles_windows.go`(`SelfInstall` 拷 dll)+ `SelfInstall` 里的
`installPlatformSideFiles` 调用(other 桩保留为 no-op 或一并删);`platform_windows.OpenTUN` 的
「需 wintun.dll 同目录」错误信息更新为「已内嵌自动释放,若仍失败见 exe 目录是否可写」。

## 4. sing-box + brook:内嵌优先(已有优先级,补 windows 内嵌即生效)

- `EnsureSingbox`/`EnsureBrook` 现有优先级 `override > 内嵌 > 下载`。windows 现在 `embedded.Singbox()`/
  `embedded.Brook()` 非空 → 走内嵌释放到 DataDir、**不下载**。无代码改动,仅靠新增内嵌资产生效。
- 结果:reality/vless/hy2/trojan/ss/vmess 六传输 + brook 在 windows **开箱即用、零下载**;
  企业 MITM 网络不再触发任何 github 下载。download 兜底仅留给无内嵌 arch(如 windows/386)。

## 5. 新增/改动文件

**内嵌资产 + 条件 embed(`internal/embedded/`)**:
- 资产:`assets/wintun_windows_{amd64,arm64}.dll`、`assets/singbox_windows_{amd64,arm64}`、
  `assets/brook_windows_{amd64,arm64}`(提交进仓库,同现有 linux/darwin 真二进制)。
- `embedded_singbox_windows_{amd64,arm64}.go`(`//go:build windows && amd64` 等 + `//go:embed`)。
- `embedded_windows_{amd64,arm64}.go`(brook,`func brook`)。
- `embedded_wintun_windows_{amd64,arm64}.go` + `embedded_wintun_other.go`(`func Wintun() []byte`、
  `func WintunVersion() string`;非 windows 返回 nil)。
- 更新 `embedded_other.go` / `embedded_singbox_other.go` 的 `//go:build` 排除约束,纳入 windows/amd64/arm64。
- 版本文件:`assets/WINTUN_VERSION`(如 `0.14.1`);sing-box/brook 复用现有 `SINGBOX_VERSION`/`BROOK_VERSION`。

**释放(`internal/provision/`)**:
- 新增 `wintun_windows.go`:`EnsureWintun`。`_other.go`:no-op。

**接线(`internal/supervisor/`)**:
- `platform_windows.OpenTUN` 首行调 `provision.EnsureWintun(exeDir, embedded.Wintun(), embedded.WintunVersion())`(`exeDir` 由 `os.Executable()` 取);失败即返回错误、不进 `CreateTUN`。

**清理(`internal/install/`)**:
- 删 `sidefiles_windows.go` + `SelfInstall` 的 `installPlatformSideFiles` 调用(`sidefiles_other.go` 一并删或留 no-op)。

**CI**:
- `embed-singbox.yml`:加 windows amd64/arm64 静态构建 job(复刻 linux 的自建,保 `with_utls,with_quic` + `CGO_ENABLED=0`,GOOS=windows)。
- `embed-brook.yml`:加 windows amd64/arm64 资产抓取。
- (新)wintun 资产:一次性从 wintun.net 取 0.14.1 的 amd64/arm64 dll 提交;版本升级走小脚本/手动。

## 6. 测试与验收

- **纯逻辑单测**(免真机,跨平台):`EnsureWintun` 的版本键缓存/幂等/原子写(用 `t.TempDir()` 当 exeDir)。
  `embedCacheKey` 已有测试模式复用。
- **交叉编译**:`GOOS=windows GOARCH=amd64/arm64 go build` 过;`embedded.Wintun()`/`Singbox()`/`Brook()`
  在 windows 构建非空(加 `embedded_test.go` 的 windows 断言,同现有 brook/singbox 测试)。
- **真机验收**(`030-SJWJ-GSR-B`,安全梯度 + 死手):
  1. 目录里**只有一个 bx.exe**(删掉所有随行 wintun.dll/sing-box.exe/config)。
  2. `bx run --test-timeout 2m -c <配置了 reality 的 config,无 singbox_bin>`:
     - bx 自动在 exe 旁生成 wintun.dll、DataDir 释放 sing-box。
     - **企业 MITM 网络下零 github 下载**(日志无下载行、无 x509 错误)。
     - reality 隧道健康、整机出口==VPS、死手还原干净。
  3. 便携:把 bx.exe 拷到一个空目录双击 `bx run` 同样跑通(自动释放 wintun.dll 到该空目录)。

## 7. 不做(YAGNI)

- 不改 wintun 加载器(不 vendor/patch `golang.zx2c4.com/wintun` 去支持 DataDir/内存加载)——
  释放到 exe 目录已够,改加载器得连带 vendor wireguard-go 的 tun,过重。
- 不做 windows/386 或其他 arch 内嵌(nil 兜底→下载即可)。
- 托盘、安装包、UAC 是子项目②③,不在此。

## 8. 风险

- **exe 目录不可写**(如从只读介质/网络共享运行)→ wintun.dll 释放失败。缓解:清晰报错指明;
  正常安装(Program Files,admin 可写)与便携(用户目录可写)均 OK,只读介质是罕见边角。
- **arm64 静态 sing-box 构建**:CI 需 windows/arm64 交叉构建 sing-box;若 upstream 构建有坑,
  arm64 先 nil 兜底→下载,不阻塞 amd64 主线(实现时可分两步)。
- **仓库体积**:新增 ~4×(28+30)MB + 4×0.4MB ≈ 230MB 真二进制进仓库(同现有 linux/darwin 已提交
  的量级)。可接受;与现状一致。
