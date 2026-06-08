# bx 开箱即用引导(zero-config bootstrap)设计

日期:2026-06-07
状态:已批准,待实现计划

## 1. 背景与问题

当前 `bx up` / `bx install` 的启动命令过长,用户记不住:

```bash
sudo bx install -c /etc/bx/config.yaml --brook /usr/local/bin/brook \
     --china-domain /etc/bx/china_domain.txt --china-cidr /etc/bx/china_cidr4.txt
```

根因:

- 每个外部依赖路径都是 CLI flag,默认值指向分散的家目录位置(`~/.nami/bin/brook`、`~/.brook/*.txt`、`~/.config/bx/`)。
- `install` 把所有 flag 烤进 systemd `ExecStart`。
- `config.yaml` 已有 `lists:` 块,但 supervisor **忽略它、改读 flag** —— config 和 flag 重复表达同一件事,用户还是得记 flag。
- brook 必须预先存在,无自动获取;china 列表静态,无自动更新。
- **chicken-and-egg 约束**:bx 没起来时机器没外网(14.37 直连 github 不通)。所以"缺了就从 github 下载 brook/列表"在最需要的机器上反而失败。

## 2. 目标

`sudo bx up`(及 `sudo bx install`)配一行 config、**零其它 flag** 即可跑起来,其余全自动:

```yaml
# /etc/bx/config.yaml —— global 模式最小配置
server: "brook://server?server=1.2.3.4%3A9999&password=…"
global: true
```

```bash
sudo bx up        # 前台
sudo bx install   # systemd 自启,开机即跑
```

systemd `ExecStart` 收敛为 `/usr/local/bin/bx up -c /etc/bx/config.yaml`。

非目标(YAGNI):多架构同时内嵌(本轮仅 linux/amd64,arm64 留作一行扩展);把 brook 作为库 in-process 运行(保持"黑盒子进程"架构);brook 运行时自升级(由 CI 在上游侧重新内嵌解决)。

## 3. 关键决策(已与用户确认)

1. **brook 来源**:`go:embed` 内嵌进 bx,首次运行解压到 `/var/lib/bx/brook` 再拉起。真·单文件、零网络依赖。
2. **china 列表**:同样内嵌一份快照作兜底;bx 起来后经隧道定期拉最新热重载。global 模式直接跳过不加载。
3. **CI**:GitHub Actions 监听 `txthinking/brook` 上游 release,有更新就自动下最新 brook(+刷新列表快照)重新内嵌进 bx。上游侧(有外网)保持最新,部署机永不碰 github。
4. 默认 config 路径 `/etc/bx/config.yaml`(非 root 回退 `~/.config/bx/config.yaml`)。
5. 内嵌仅 linux/amd64。
6. 列表刷新间隔 24h。

## 4. 架构

### 4.1 组件

**`internal/embedded`(新)** —— 内嵌资产
- `//go:embed assets/brook_linux_amd64` `//go:embed assets/china_domain.txt` `//go:embed assets/china_cidr4.txt`
- 暴露:`Brook() []byte`、`ChinaDomain() []byte`、`ChinaCIDR() []byte`、`BrookVersion() string`(读自内嵌的 `assets/BROOK_VERSION`)。
- 是 CI 唯一改写的目录。

**`internal/provision`(新)** —— 运行期物料落盘到 `DataDir`(默认 `/var/lib/bx`)
- `EnsureBrook(dataDir, cfgOverride string) (path string, err error)`:
  - 若 `cfgOverride` 非空且存在 → 直接用(用户显式覆盖,尊重之)。
  - 否则解压内嵌 brook 到 `<dataDir>/brook`;写 `<dataDir>/.brook-version`。当 `.brook-version` 与 `embedded.BrookVersion()` 不一致时**重新解压**(随 bx 升级自动换 brook)。文件权限 0755。
  - 解压用「写临时文件 + rename」保证原子,避免覆盖正在执行的 brook 触发 ETXTBSY/损坏。
- `EnsureLists(dataDir string) (domainPath, cidrPath string, err error)`:
  - 缺失时解压内嵌快照到 `<dataDir>/china_domain.txt` `<dataDir>/china_cidr4.txt`(0644)。已存在(可能是刷新过的新版)则不覆盖。

**列表刷新器(supervisor 内 goroutine)** —— "规则自动更新"
- 仅非 global 模式启动。
- 隧道 healthy 后,每 `interval`(默认 24h)经 **brook socks5**(用已有 proxyDialer 构 `http.Client`,绕过 github 直连封锁)拉列表源 `https://txthinking.github.io/bypass/china_domain.txt` 与 `china_cidr4.txt`(与 CI 内嵌快照同源)→ 原子写入 `DataDir` → 热重载路由。
- 拉取失败非致命:log + 保留旧列表,等下个周期。

**路由热重载** —— `dialer.Dialer` 持有 `atomic.Pointer[route.Router]`(由当前裸字段 `Router *route.Router` 改造),刷新器重建 Router 后原子 swap。读路径(`Dial`)无锁取指针。兑现此前 `bx reload` 的 notImpl 语义(本轮先做内部热重载,CLI `reload` 可后续接上)。

**config 成为单一事实源**
- `config.Config` 新增:`Brook string`(可选,空=用内嵌)、`DataDir string`(默认 `/var/lib/bx`)、`Lists` 扩展 `AutoUpdate bool`(默认 true)、`Interval` 字段(默认 24h)。
- 说明:`bx up` 本就需 root(TUN/路由),故 `DataDir=/var/lib/bx` 始终可写;config 路径回退到 home 只为非 root 下 `bx status` 等只读命令找得到配置,与 `up` 的 root 前提不冲突。
- supervisor 改为**读 `cfg.Lists` / `cfg.Brook`**,不再依赖 flag。
- CLI flag 保留为**可选覆盖**(向后兼容),但都不再是必需;空值表示"用 config/内嵌"。

### 4.2 数据流(bx up)

```
加载 config(默认 /etc/bx/config.yaml)
  → provision.EnsureBrook → brookPath
  → 非 global: provision.EnsureLists → 列表路径;global: 跳过
  → BuildRouter(cfg, 列表)  [已含 DefaultPrivateCIDRs 内核分流 + PrivateDirect]
  → tunnel.NewBrook(brookPath, server) 起隧道
  → TUN + 引擎 + 路由劫持(含 private→main 内核分流)
  → 非 global 且 AutoUpdate: 起列表刷新 goroutine(待 healthy 后周期刷新→热重载)
  → 阻塞至信号/deadman
```

### 4.3 CI:`.github/workflows/embed-brook.yml`

触发:`schedule`(每日)+ `workflow_dispatch`。

1. 取 `txthinking/brook` 最新 release tag,与 `internal/embedded/assets/BROOK_VERSION` 比对。
2. 若更新:下载 `brook_linux_amd64` 覆盖内嵌;从 txthinking bypass 列表刷新 `china_domain.txt`/`china_cidr4.txt` 快照;写新版本号到 `BROOK_VERSION`。
3. `go build` + `go test ./...` 验证可编译可过测。
4. 提交(或开 PR);可选 build 并附 `bx` release 产物。

## 5. 配置 schema(最终)

```yaml
server: "brook://…"        # 必填
global: true               # 可选,默认 false
killswitch: true           # 可选,默认沿用现状
bypass:                    # 可选;RFC1918 已由内核 private→main 自动绕过,通常不需要
  - 203.0.113.5/32         #   仅当管理地址是公网 IP 时才需要
brook: ""                  # 可选;默认用内嵌 brook
data_dir: /var/lib/bx      # 可选;默认 /var/lib/bx
dns:                       # 可选,默认沿用现状
  china: 223.5.5.5
  fakeip_cidr: 198.18.0.0/15
lists:                     # 可选;非 global 才用
  auto_update: true        #   默认 true
  interval: 24h            #   默认 24h
  china_domain: ""         #   默认用 data_dir 下解压/刷新的快照
  china_cidr: ""
```

global 模式最小可用配置就两行:`server` + `global: true`。

## 6. 错误处理

| 场景 | 处理 |
|---|---|
| 内嵌 brook 解压失败 / DataDir 不可写 | 致命,清晰报错(无 brook 跑不了) |
| config.brook 指定但文件不存在 | 致命,报"指定 brook 路径不存在" |
| 非 global 但无列表且解压失败 | 降级:空列表(中国暂走代理)+ 警告,不致命;等刷新器补 |
| 列表刷新拉取失败 | 非致命:log + 保留旧列表,下周期重试 |
| 热重载 swap | 原子指针,读路径不阻塞;失败保留旧 Router |

## 7. 测试

- `internal/embedded`:断言三份资产字节非空、`BrookVersion()` 非空。
- `internal/provision`:首次解压创建文件;版本号一致时不重复解压;版本号变更触发重解压;已存在列表不被覆盖;原子 rename 行为(临时文件→目标)。
- `config`:默认值填充(DataDir、AutoUpdate、Interval);路径解析回退(root→/etc/bx,非 root→home)。
- `dialer`:`atomic.Pointer[Router]` 并发 Dial 与 swap 无 data race(`-race`)。
- `cli`/`install`:`ExecStart` 收敛为仅 `-c <path>`,不再含 brook/china flag。
- CI:`workflow_dispatch` dry-run 跑通比对逻辑。

## 8. 实现顺序(供 writing-plans 细化)

1. `internal/embedded` + 把现有 14.37 的 brook 与 china 列表作为初始内嵌资产入库。
2. `config` 扩字段 + 默认值 + 路径回退。
3. `internal/provision`(EnsureBrook / EnsureLists)+ 单测。
4. supervisor 接 provision、读 cfg(去 flag 依赖);`dialer` 路由 atomic 化。
5. 列表刷新 goroutine + 热重载。
6. cli/install 瘦身(flag 转可选覆盖,ExecStart 收敛);更新 README 快速开始。
7. `.github/workflows/embed-brook.yml`。

## 9. 兼容性 / 迁移

- 旧 config(无新字段)照常工作:新字段全有默认。
- 旧 `install`(烤了一堆 flag 的 ExecStart)仍能跑;重新 `bx install` 即收敛为短命令。
- CLI flag 全部保留为覆盖,老脚本不破。
