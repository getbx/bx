# bx 傻瓜化命令模型(setup / blink / up / down / run)设计

日期:2026-06-07
状态:已批准(对话中敲定),待实现计划

## 1. 背景与目标

即便 brook/列表已内嵌、零 flag,普通用户仍要手写 YAML、懂 `/etc/bx`、`global`、`killswitch`、记 `down/up/install` 顺序,且 `bx up` 前台阻塞——对非技术用户仍麻烦。

目标:把"从零到跑"压成 **scp 二进制 + 一条 `sudo bx setup` + `sudo bx up`**,日常只用 `up`/`down`。用户全程不碰 YAML、看不到 brook/IP/密码明文。

## 2. 已确认的决策(对话敲定)

1. **链接用 bx 自有 `blink://` 别名**:本质是 brook 链接换壳(base64url 包一层),对用户隐藏 brook。运行时仍解码回 brook,底层代码零改动。
2. **开机自启绑在 up/down 上**:`up = systemctl enable --now`(开+自启),`down = systemctl disable --now`(关+取消自启)。直觉:开就一直开(含重启),关就彻底关。
3. `setup` 只配置+装服务+连通检测,**不启动、不自启**。
4. `up` 后台(systemd),`run` 前台带 log(调试/首跑;systemd unit 内部也跑它)。

## 3. 命令表(最终)

| 命令 | 行为 |
|---|---|
| `sudo bx setup blink://...` | 解码 blink→brook 链接;写 `/etc/bx/config.yaml`(`server` + `global:true` + `killswitch:true`);装 systemd unit(`ExecStart=<bin> run -c /etc/bx/config.yaml`,**不 enable、不 start**);**连通检测**(临时起 brook 探服务器,报 ✓/✗ + 延迟)。已存在 config 需 `--force` 覆盖。 |
| `sudo bx up` | `systemctl enable --now bx`(后台起 + 开机自启)。无 `/etc/bx/config.yaml` → 提示"先 sudo bx setup"。 |
| `sudo bx down` | `systemctl disable --now bx`(停 + 取消自启)。 |
| `sudo bx run [flags]` | 前台跑、实时 log(= 旧 `bx up` 的前台行为)。systemd unit 内部命令。保留 `--config/--global/--test-timeout/--tun*/--probe/--brook/--china-*` 覆盖 flag。 |
| `bx status` | 面板(不变)。 |
| `bx blink <brook://...>` | 由 brook 链接生成 `blink://...`(管理员用,发给用户)。普通用户。 |
| `sudo bx uninstall` | 卸载服务(不变)。 |

用户视角:**① scp bx ② `sudo bx setup blink://...`(见 ✓ 连通)③ `sudo bx up`**。日常 `up`/`down`。

## 4. blink 格式

`blink://` + `base64url(完整 brook 链接)`(无 padding,RawURLEncoding)。

- `blink.Encode(brookLink string) string`:`"blink://" + base64.RawURLEncoding.EncodeToString([]byte(brookLink))`。
- `blink.Decode(s string) (string, error)`:校验前缀 `blink://`;去前缀后 RawURLEncoding 解码;校验解出的链接以 `brook://` 开头;否则报错。
- 纯函数,无外部依赖,完整单测(round-trip、坏 base64、错 scheme、非 brook 内容)。
- 新包 `internal/blink`。

## 5. install 包拆分

当前 `install.Install(execStart)` = 写 unit + daemon-reload + enable + restart(会启动)。setup 需要"装但不启动",up/down 需要 enable/disable。拆成:

- `install.WriteUnit(execStart string) error` — 写 unit 文件 + `daemon-reload`。**不 enable、不 start。**
- `install.Enable() error` — `systemctl enable --now bx`。
- `install.Disable() error` — `systemctl disable --now bx`。
- `install.Uninstall() error` — 不变(disable --now + rm + daemon-reload)。
- `install.UnitText(execStart)` — 不变。

## 6. setup 流程(新包 `internal/setup`)

`setup` action 顺序:
1. `link, err := blink.Decode(arg)`。
2. `brookPath, err := provision.EnsureBrook(dataDir, "", embedded.Brook(), embedded.BrookVersion())`(dataDir 默认 `/var/lib/bx`;解压内嵌 brook 供探测/运行用)。
3. **连通检测** `setup.ProbeServer(brookPath, link, probeTarget, timeout) (latencyMS int64, err error)`:用 `tunnel.NewBrook(brookPath, link, probeTarget)` 起隧道、等 `Healthy()`(带超时)、读延迟、`Stop()`。**不建 TUN**。报 ✓ 延迟 / ✗ 原因。探测失败默认仅警告(仍写配置),除非加 `--strict` 才中止——让用户能在不通的网络下先配好。
4. `setup.WriteConfig(path, link string, force bool) error`:若 `path` 存在且 `!force` → 报错"已存在,加 --force 覆盖";否则写:
   ```yaml
   server: "<brook link>"
   global: true
   killswitch: true
   ```
   (bypass 不写——私网已由内核 ip-rule 自动绕过。)
5. `install.WriteUnit(buildExecStart(bin, path))`(unit 就位,未启动)。
6. 打印下一步:`sudo bx up` 启动 + 自启。

`WriteConfig` 用 `gopkg.in/yaml.v3` 序列化一个 minimal struct(server/global/killswitch),保证可被 `config.Parse` 读回。可单测(写临时文件→Parse→断言)。`ProbeServer` 需活 brook,不单测(部署时验证)。

## 7. cli 改动

- 命令注册:新增 `setup`、`run`、`blink`;`up`/`down` 改语义;保留 `status`/`uninstall`;`install` 折叠(可留为 `setup` 的别名或删;本设计删除 `install` 子命令,功能并入 `setup`)。
- `runAction` = 旧 `upAction`(`supervisor.Run`)。`run` 用 `upFlags()`(改名 `runFlags()`)。
- `upAction`:校验 `/etc/bx/config.yaml` 存在(或 unit 已装)→ `install.Enable()`;否则提示先 setup。
- `downAction`:`install.Disable()`(替换旧的读 pid SIGTERM——那套归 `run` 的 Ctrl-C)。
- `setupAction`:走第 6 节流程;flag `--force`、`--strict`、`--probe`(默认 `1.1.1.1:443`)。
- `blinkAction`:`fmt.Println(blink.Encode(c.Args().First()))`,校验参数以 `brook://` 开头。
- `buildExecStart(bin, configPath)`:改为 `<bin> run -c <configPath>`(`up`→`run`)。

## 8. 错误处理

| 场景 | 处理 |
|---|---|
| blink 解码失败 / 非 brook 内容 | setup/blink 报清晰错误并退出 |
| 连通检测失败 | 默认警告 + 仍写配置(`--strict` 才中止) |
| config 已存在且无 `--force` | 报错,提示加 `--force` |
| `bx up` 但未 setup | 提示"先 sudo bx setup blink://..." |
| 非 root 跑 setup/up/down | systemctl/写 `/etc/bx` 会失败,错误透传 |

## 9. 测试

- `internal/blink`:Encode/Decode round-trip;坏 base64;错 scheme;解出非 brook;`bx blink` 输出可被 `Decode` 还原。
- `internal/setup`:`WriteConfig` 写出的 YAML 能被 `config.Parse` 读回且 server/global/killswitch 正确;`force=false` 且文件存在时报错;`force=true` 覆盖。
- `internal/install`:`UnitText` 含 `ExecStart=... run -c ...`(更新断言);`WriteUnit` 不调用 enable/start(若提取命令序列做断言)。
- `cli`:`buildExecStart` 返回 `<bin> run -c <path>`;`blinkAction` 对非 brook 参数报错。
- `ProbeServer`、`Enable/Disable`、`up/down` action:集成/部署验证(需 root + 活 brook),不单测。

## 10. 实现顺序

1. `internal/blink`(Encode/Decode + 单测)。
2. `internal/install` 拆 WriteUnit/Enable/Disable(+ 更新 UnitText 测试)。
3. `internal/setup`(WriteConfig + ProbeServer + WriteConfig 单测)。
4. cli:`buildExecStart`→run、新增 setup/run/blink、改 up/down、删 install 子命令(+ 单测)。
5. README 更新傻瓜流程 + blink 说明。

## 11. 兼容性

- 老 `bx up`(前台)语义变为 `bx run`;老 `bx install` 并入 `setup`。项目尚无外部用户,可接受破坏性改名。
- 现有 `/root/.config` 或自带 config 仍可被 `bx run -c <path>` 直接前台跑。
- 底层运行时(supervisor/tunnel/brook 解析)零改动:blink 仅在 setup 入口解码成 brook 写进 config。
