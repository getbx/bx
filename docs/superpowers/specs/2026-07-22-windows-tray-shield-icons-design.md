# Windows 托盘盾牌图标 + 4 态设计

- 日期:2026-07-22
- 范围:**纯 Windows**(Go + winres 资源)。不改 macOS。
- 前置:子项目②托盘 App(`internal/tray`,`bx tray`)+ 子项目③打包(`winres/` + `rsrc_windows_*.syso`)已在仓库(HEAD `1777910`)。

## 背景与动机

Windows 上 bx 目前对小白不友好的两点:

1. **托盘图标是无名彩色圆点**(`internal/tray/icons/{protected,off,attention}.ico` = 绿/灰/红实心圆)。一个裸绿点不传达"这是什么",没有代理/VPN 类工具通用的"保护"含义,托盘里不具辨识度。
2. **与 macOS 不一致**:macOS `BxMenu` 菜单栏用 `NSImage(systemSymbolName: "shield")` + 状态色(`apps/macos/BxMenu/Sources/BxMenu/main.swift:141`、`StatusIndicator.swift`),是"盾牌+状态色";Windows 却是裸圆点。

本设计把 Windows 图标改为**盾牌 + 小写 "b"**,并把托盘状态从 3 态扩到 4 态,与 macOS 的"绿/黄/红/灰"状态语言对齐。

## 统一状态语义模型(跨平台契约)

一套 4 色语义,mac + Windows 共同遵守:

| 色 | 语义 | Windows 触发 | macOS 现状 |
|---|---|---|---|
| 🟢 绿 `#22C55E` | 已保护(最优) | 隧道健康(**含走备用传输,不打扰用户**) | connected |
| 🟡 琥珀 `#F5B414` | 有可用更新(要动手、不紧急) | `bx update --check` 报 `available` | updateNeeded→yellow |
| 🔴 红 `#EF4444` | 故障断网(critical) | 隧道不健康(fail-closed) | failed |
| ⚪ 灰 `#949CA4` | 未激活 | 关 / 未配置 | off / setupNeeded / missing |

**关键决策——自动容灾保持绿色**:automatic failover(切到 brook 备用传输)是设计上让用户"不用管"的自愈行为;每次 fallback 弹琥珀等于制造无谓焦虑。故走备用传输**恒绿**,不降级提示。琥珀只留给"有可用更新"这一"用户该动手、但不紧急"的信号。

**两端天然对齐**:琥珀=有更新,而 macOS 本就用黄色表示 updateNeeded。故本设计**无需改 macOS,也无 follow-up 欠账**;Windows 侧只需补"托盘查更新"这一小块。

## 图标美术

- **统一形制**:heater 盾牌(平顶+圆角、两侧下收、底部圆润收尖)+ 白色小写 "b" 居中。所有面(资源管理器 exe 图标 / 安装包 / 开始菜单 / 托盘)同一个盾,只换填充色。
- **配色**:见上表四色。白 `#FFFFFF` 的 "b"。
- **交付物**:
  - `winres/icon.png`(256²)+ `winres/icon16.png`(32²):**绿盾+b**(exe/app 图标)。
  - `internal/tray/icons/{protected,warning,failed,off}.ico`:每个 `.ico` 内含多尺寸 **16/20/24/32**,对应四色。`protected`=绿、`warning`=琥珀、`failed`=红、`off`=灰。
  - 重生成 `rsrc_windows_amd64.syso` / `rsrc_windows_arm64.syso`(`go generate ./...`,提交)。
- **渲染约束**(托盘 16px 立得住):粗壮实心盾、透明底、高对比;"b" 占盾高约 46%;不用细描边(同色任务栏上会消失)。深/浅色任务栏均适用(实心色填充)。

## 状态模型改动

### `internal/tray/state.go`

- `TrayState` 新增 `StateWarning`(置于 `StateProtected` 之后、`StateAttention` 之前或独立值均可,取值稳定即可)。
- 新增合成函数(替换/扩展 `trayStateFrom`),输入增加 `updateAvailable bool`:

  ```
  trayStateFrom(svcRunning, configExists, tunnelHealthy, updateAvailable) TrayState
  ```

  **优先级(消除歧义)**:
  1. `!configExists` → `StateNotSetup`(灰)
  2. `!svcRunning` → `StateOff`(灰)
  3. `svcRunning && !tunnelHealthy` → `StateAttention`(红)——**故障优先于更新**
  4. `svcRunning && tunnelHealthy && updateAvailable` → `StateWarning`(琥珀)
  5. 否则(健康且无更新)→ `StateProtected`(绿)

- `menuItemsFor`:`StateWarning` 态在现有"断开"基础上多一项 **"更新到最新版"**(见菜单)。

### `internal/tray/icons_windows.go`

- 新增 `//go:embed icons/warning.ico` → `iconWarning`;`icons/attention.ico` 重命名为 `icons/failed.ico`(语义更准;同步 embed 与文件)。
- `iconFor` 四路:`StateProtected`→绿、`StateWarning`→琥珀、`StateAttention`→红、其余→灰。

### `internal/tray/status.go`(`detectState`)

- 增加**节流更新检查**:
  - 新增纯函数 `shouldCheckUpdate(lastChecked, now time.Time, interval time.Duration) bool`(注入时间,可测)。间隔默认 **6h**。
  - `detectState` 维护 `lastUpdateCheck time.Time` + 缓存 `updateAvailable bool`(存于托盘轮询循环的持有者,不是每次 spawn)。到期才 spawn `bx update --check --json`、解析 `available`;未到期用缓存。
  - 解析用 `internal/cli/update.go` 的 `updateCheckReport`(`{current,latest,available,verified}`)同形 struct(tray 侧本地定义 struct + json tag,避免跨包耦合;字段名对齐)。
  - 更新检查失败(网络/MITM)→ 视为 `updateAvailable=false`,**绝不**让更新检查失败影响绿/红/灰主判定(best-effort)。
  - **职责划分(消歧义)**:节流状态(`lastUpdateCheck time.Time` + 缓存 `updateAvailable bool`)由**托盘轮询持有者**(`internal/tray/tray_windows.go`)持有;轮询循环每 tick 用 `shouldCheckUpdate` 判断是否到期,到期才 spawn `bx update --check --json` 刷新缓存。`detectState` 签名改为 `detectState(exePath, configPath string, updateAvailable bool) (TrayState, StatusDetail)`——即**由调用方把更新检查结果传入**,`detectState` 自身不发起网络更新检查(保持它只做 svc 只读 + status spawn)。纯逻辑 `trayStateFrom`/`shouldCheckUpdate`/`parseUpdateCheckJSON` 与 syscall 无关、可 Linux 单测。

### 菜单:"更新到最新版"

- `StateWarning` 态菜单多一项"更新到最新版"。
- 点击 → 走托盘既有的 **per-action UAC 提权**模型(`elevateRun` / `ShellExecuteW` verb `runas` + `SW_HIDE`),提权跑 `bx update`(下载已签名包 + 替换 `C:\Program Files\bx\bx.exe` + 重启服务)。动作前 `MessageBox` 确认(与现有连/断/重启一致)。
- 复用现有提权入口,不新造逻辑。

## 不改动 / 不变量

- winres.json 的 `execution-level` 恒 `as invoker`(不动)。version 占位 `0.0.0.0`,release CI 覆盖(不动)。
- 图标只换美术,`RT_GROUP_ICON`/manifest/version 结构不变。
- 不改 macOS。不改隧道/路由/数据面。

## 测试

- **纯逻辑单测(Linux 可跑,免 root)**:
  - `state_test.go`:`trayStateFrom` 五档优先级——含更新+健康→warning、不健康+有更新→**attention(红优先)**、未运行+有更新→off、走备用(健康)→protected 等边界。
  - `status_test.go`:`shouldCheckUpdate` 节流(注入 now/lastChecked/interval);`parseUpdateCheckJSON` 解析 available/verified,坏 JSON→false。
- **构建/资源守卫**:`go build`(windows amd64/arm64)+ 现有 `.syso` 守卫测试;`go generate` 后 `go-winres extract` 抽出图标核对(dev 端)。
- **真机待验(交互桌面,非 SSH headless)**:托盘四态图标外观、琥珀态"更新"菜单提权流程、exe/开始菜单图标显示。SSH 起的托盘落会话 0、交互桌面看不到,需人在机器旁。

## 交付清单(供 writing-plans 拆解)

1. 生成盾牌图标资产(`icon.png`/`icon16.png` + 4 个多尺寸 `.ico`)。
2. `go generate` 重生成并提交 `.syso`。
3. `state.go`:`StateWarning` + `trayStateFrom` 扩展(TDD)。
4. `status.go`:`shouldCheckUpdate` 节流 + 更新检查解析(TDD)。
5. `icons_windows.go`:`warning.ico` embed + `attention→failed` 重命名 + `iconFor` 四路。
6. 托盘轮询持有者接入节流更新检查 + 传 `updateAvailable`。
7. 菜单"更新到最新版"(warning 态)+ 提权 `bx update`。
8. windows amd64/arm64 交叉编译 + vet + 单测全绿。

## 关键决策记录

- **盾+"b" 而非裸圆点**:盾=保护(通用安全隐喻)、b=品牌辨识,一图兼顾;与 macOS 盾牌一致又多了辨识度。
- **failover 恒绿**:自愈行为不打扰用户;琥珀不表示降级。
- **琥珀=有更新**:唯一"该动手、不紧急"的信号,与 macOS 天然对齐,零 follow-up。
- **更新检查节流 6h**:避免 3s 状态轮询砸更新端点;失败 best-effort 不影响主判定。
- **纯 Windows**:范围收窄、好 review、好测。
