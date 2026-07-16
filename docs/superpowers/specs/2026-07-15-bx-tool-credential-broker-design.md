# bx Tool Credential Broker 设计

Status: APPROVED-FOR-SPEC-REVIEW (2026-07-15)

## 背景

个人用户经常直接把 Cloudflare、Hugging Face、小众 SaaS 等 Tool API
token 粘贴给 Codex、Claude Code 或其他 agent。这让 token 进入 LLM 上下文、
shell history、转录、进程环境或日志，也让 prompt injection 可以直接复制并外传
token。

bx 的定位始终是个人产品。本功能不做团队账号、组织 RBAC、费用分摊或供应商
业务权限模型，只做一个个人 Tool Credential Broker：把可读取、可复制的秘密，变成
不可读取、只能发往指定 HTTPS origin 的本地能力。

## 核心产品承诺

bx 保证：

1. token 值不进入 agent/LLM 上下文、配置、环境变量、argv 或日志。
2. 每个 token 精确绑定一个 HTTPS origin（scheme + hostname + port），绝不向其他
   origin 发送。
3. agent 只能要求 bx 使用某个能力，不能显示、复制、导出或变更已存
   token 的 origin。
4. 用户可以在一个极简界面中暂停、替换、删除 token，并查看最近使用记录。
5. broker 未启用或不可用时 fail closed，不回退到让 agent 持有明文 token。

bx 不保证：

1. 不从 opaque token 猜测供应商 scope。
2. 不通用理解不同厂商 endpoint 的业务语义或危险程度。
3. 不把全权限 token 自动变成供应商侧的最小权限 token。真正的业务缩权仍需用户在
   供应商处创建 least-privilege token。
4. 不防 confused deputy：若 agent 调用了绑定 origin 上的破坏性 API，且上游
   token 允许，bx 不能靠通用规则判断并阻止。

## 产品边界

本功能对用户叫 **bx Tool Keys**，是 bx 的可选 companion service，不是动态加载进 bx
网络守护进程的 Go plugin。

- `bx core`：现有 TUN、路由、DNS、传输和 kill-switch，不读取 token，不改数据面。
- `bx keyd`：使用同一 bx 二进制的独立守护进程模式，持有 token、校验 origin、
  注入认证并代理 HTTP 请求。
- `BxMenu`：提供安全粘贴、暂停、替换、删除和最近使用界面。
- `bx mcp`：保留现有 agent surface，始终注册窄的 Tool Keys 目录/凭据请求/API
  调用工具；keyd 未启用时只返回结构化 `credential_broker_unavailable` 和启用指引，
  不要求用户在对话里粘贴 token。

Tool Keys 默认不启用。它的安装、运行、状态、数据目录和日志与 bx core 独立。
keyd 停止、升级失败或数据损坏时，bx 网络保护必须继续正常运行。

V1 面向个人 macOS + BxMenu。keyd 核心保持 Go 平台无关边界，但 Windows/Linux UI、
安装和凭据存储不在 V1 范围。

### 仓库与依赖边界

V1 放在现有 `github.com/getbx/bx` 仓库和 Go module 中，不新建仓库、子 module 或通用
插件加载器。同仓库复用 BxMenu、`bx mcp install`、签名、打包、升级和 launchd 流程；
运行时隔离由独立进程、service label、socket、数据目录和单向依赖保证。

```text
bx/
├── internal/toolkeys/                 Credential、存储、broker、redaction、audit、LocalAPI
├── internal/mcp/tools_toolkeys.go     只通过 toolkeys LocalAPI client 适配 MCP
├── internal/cli/toolkeys.go           enable/status/disable/keyd 命令线接线
└── apps/macos/BxMenu/Sources/BxMenu/ToolKeys/
                                         安全录入、pending prompt 与管理 UI
```

依赖方向必须是：

```text
BxMenu  ──────→ toolkeys LocalAPI
bx mcp  ──────→ toolkeys LocalAPI

bx supervisor/core ─X→ toolkeys
toolkeys           ─X→ bx supervisor/core
```

`internal/toolkeys` 不 import `internal/supervisor`、`internal/tun`、`internal/dns`、`internal/route`、
`internal/dialer` 或 `internal/tunnel`。`internal/supervisor` 及数据面包不 import
`internal/toolkeys`。所有跨边界交互通过独立 Unix LocalAPI，不共享 Go 对象或进程内状态。

`api-key-broker` 目录保留为早期研究/交接材料，不承载生产代码。只有当 Tool Keys 需要
脱离 bx 独立安装、形成独立跨平台发布节奏或出现多个第三方扩展时，才另行设计
独立仓库/插件协议。

## 数据模型

一条凭据记录只包含安全边界和管理所需的最小信息：

```text
Credential
  id              随机不透明 ID
  label           默认从 hostname 生成，用户可选改名
  origin          精确 HTTPS origin，不支持默认通配子域
  secret          token 值，只在 keyd 秘密存储中
  auth_hint       默认认证注入方式，非秘密
  enabled         暂停/启用
  created_at
  rotated_at
  last_used_at
```

认证注入描述与 secret 分离：

```text
AuthHint
  type            bearer | header | query
  name            header/query 名；Bearer 固定为 Authorization
```

AuthHint 不是秘密，可由 agent 根据 API 文档提议。keyd 只接受上述有限枚举，
不接受 agent 提供的任意字符串模板。query 注入在 UI 中显示「可能进入上游 URL
日志」警告，但不阻断确实只支持 query key 的 API。

origin 在创建时规范化：scheme 固定为 `https`，hostname 小写并做 IDNA 规范化，
空端口视为 443，显式 `:443` 与空端口归一。origin 输入不得包含 userinfo、path、
query 或 fragment。

V1 不存储 method/path 权限表。方法、路径、供应商 recipe 和「询问是否允许
DELETE」均不进入默认模型。

## 用户录入体验

### 默认：agent-assisted pairing

agent 已在读 API 文档，因此由 agent 提供非秘密的 origin、认证建议和用途：

```text
bx_credential_request(
  origin="https://api.example.com",
  auth_hint={type:"bearer"},
  reason="调用 Example API 创建任务",
  docs_url="https://docs.example.com/api/auth"
)
```

`bx mcp` 将请求转给 keyd；keyd 创建一个不含 secret 的 pending request，BxMenu 弹出：

```text
Codex 需要一个 API 凭据

发送目标: api.example.com
认证方式: Bearer（Codex 根据文档建议）
用途: 创建任务

请只粘贴为上述服务签发的 key。
API key: [secure field]

[Cancel] [Save and continue]
```

用户只输入 token 并确认目标 hostname。label 从 hostname 自动生成，不要求用户
填 scope、method、path、OpenAPI 或供应商类型。

保存后 agent 重试原请求。上游 401/403 作为正常 API 结果返回给 agent，bx
不解释为 scope 模型。若 auth hint 错误，agent 可以在不接触 token 的情况下用另一
个受支持 AuthHint 重试；origin 始终不变。

pending request 超过 10 分钟自动失效，取消或失效都不创建空凭据。来自 agent 的
origin、reason 和 docs URL 在 UI 中明确标成「agent 提议」，不冒充 bx 验证结果。

### 手动入口

BxMenu 保留两个手动入口：

1. **粘贴 curl**：在本地安全解析区中粘贴一条已知可用的 curl。只支持 HTTPS URL
   和有限认证形式，自动拆出 origin、AuthHint 和 secret；原文不落盘、不进日志。
2. **手动添加**：只要求 HTTPS origin 和 token。AuthHint 使用 Bearer 默认，可在一个折叠的
   「高级认证」区修改为 header/query。

不单独提供「只粘贴 token」手动模式：对未知 token，origin 是防外传的最小必要
信息，不能由 bx 猜测或省略。

## Agent 调用面

用户只做一次 `bx mcp install`。新增、替换、暂停或删除 token 不需要重新配置
Codex/Claude。

Tool Keys 在现有 `bx mcp` 中增加三个窄工具：

```text
bx_credentials_list()
bx_credential_request(origin, auth_hint, reason, docs_url?)
bx_api_request(credential_id, method, path, query?, headers?, body?, auth_hint?)
```

- `bx_credentials_list` 只返回 ID、label、origin、enabled 和最近使用时间。
- `bx_credential_request` 只创建需用户在 BxMenu 完成的 pending request，永不接收 token。
- `bx_api_request` 只接收相对 path，不接收绝对 URL、Host header、raw auth template
  或 secret。keyd 强制使用 credential 中的 origin。`auth_hint` 省略时使用已存
  默认值；agent 可为一次重试提供受限 AuthHint，不持久更改默认值或 origin。

V1 请求/响应只支持 JSON 和 UTF-8 text，单向 body 上限 8 MiB。不支持 multipart
文件上传、二进制响应、SSE 或无界流式转发，以保证响应在进入 agent 前可完整
扫描和打码。这些格式属于后续兼容 slice。

V1 不默认开启 `127.0.0.1` TCP HTTP 代理；MCP 适配器通过 Unix socket 访问 keyd，
减少本机全进程可调用面。需要让传统 SDK/CLI 改 Base URL 的兼容代理属于后续独立
slice，不拖累 V1 个人 agent 主线。

## keyd 架构与存储

macOS V1 使用 launchd 运行独立 root-owned `bx keyd` 进程。它与 bx core 守护进程是
不同的 process/service label，不共享内存、生命周期或状态文件。

- 密钥目录：`/Library/Application Support/bx/toolkeys/`，`root:wheel` + `0700`。
- 凭据文件：原子写入，`root:wheel` + `0600`，不包含任何 agent-readable 镜像。
- 磁盘离线保护依赖用户的 FileVault；V1 不用与密文同机可读的本地包装密钥制造
  虚假的「二次加密」承诺。
- 管理/调用通道：HTTP/1.1 over Unix socket，复用 bx 现有的 LocalAPI + peer credential 范式，
  但使用独立 socket path 和路由集。
- socket 回复中没有任何读取 secret 的路由或字段。

V1 威胁模型是阻止普通用户身份运行的 agent 读取 token，不防 root、内核级恶意程序、
输入 token 前已劫持用户界面的恶意软件或主动读取系统剪贴板的程序。

## 代理安全不变量

keyd 发送每个请求时必须满足：

1. 只允许 HTTPS origin。localhost/private CA/IP literal 不在 V1。
2. 请求 path 必须是以 `/` 开头的相对 origin path，拒绝 scheme-relative URL、绝对 URL、
   userinfo 和 CR/LF。
3. 强制 Host/SNI/TLS 证书验证使用已绑定 origin，agent 不能覆盖 Host。
4. 删除 agent 传入的 `Authorization`、`Proxy-Authorization`、`Cookie`、
   常见 API-key header 和 hop-by-hop headers，再由 keyd 按有限 AuthHint 注入唯一凭据。
5. HTTP 客户端不自动 follow redirect。任何 3xx 都将不含凭据的状态和清理后 Location
   返给 agent。
6. 审计日志只记时间、credential ID/label、origin、method、path（query 不记）、
   status、耗时和调用面；不记 request/response body 或认证头。
7. 错误包装在返回前扫描已存 secret 的精确值；任何命中都替换为 `<redacted>`。
8. JSON 响应中名为 `token`、`api_key`、`secret`、`password`、`private_key`、
   `client_secret`、`access_token` 或 `refresh_token`（大小写不敏感）的字段值在
   进入 agent 上下文前打码；同时剥离响应认证/Cookie headers。
9. 暂停、删除或轮换与新请求之间原子生效。已经发送的 in-flight 请求可以完成；
   之后的请求使用新状态。

## 管理体验

BxMenu 的 Tool Keys 页默认只显示：

```text
Example
api.example.com
最近使用: 2 分钟前 · Codex
状态: 可用

[Pause] [Replace key] [Delete]
```

不提供「Show」、「Copy」或明文导出。Replace 要求用户重新粘贴 token，原子替换后
立即丢弃旧值。Delete 删除 secret、元数据和审计中的 label/origin 可识别引用；
聚合计数可保留为不可回溯的数字。

默认审计保留 30 天。用户可在页面上一键清空。V1 不做云端同步、多设备同步、
共享凭据或团队审计。

界面中的「Codex」等调用面名是本机 best-effort 审计标签，来自调用的 MCP/CLI
入口和本场 peer/process 信息，不是 Tailscale 式的密码学用户身份，不用作授权边界。

## 错误与恢复

- **无匹配凭据**：`bx_api_request` 返回结构化 `credential_required`，建议 agent 调用
  `bx_credential_request`；不提示用户把 token 粘贴进对话。
- **pending**：返回 `user_action_required` 和不含 secret 的 request ID，agent 等待用户完成
  BxMenu 操作后重试。
- **401/403**：传递清理后的上游状态与响应，不自动判定 token scope。
- **3xx**：不跟随，返回 `redirect_not_followed`。
- **keyd 不可用**：MCP 工具返回 `credential_broker_unavailable`；bx core 状态和数据面不受影响。
- **密钥文件损坏**：keyd 拒绝启动调用面，保留原文件供人工恢复，不自动清空或覆盖。
- **轮换中断**：旧 token 继续有效；只在新值持久化成功后原子切换。

## 测试与验收

### 纯逻辑与存储

- unknown provider 的 agent-assisted flow 只要求用户提供 token，origin/AuthHint 来自已显示的
  agent proposal。
- Credential 的 origin 不可修改；变更 origin 必须创建新 Credential 并重新粘贴 token。
- root-owned 目录/文件权限、原子写入、暂停、轮换、删除和损坏恢复有单测。
- 从磁盘、catalog、审计、错误、JSON 响应和 MCP 返回中搜索测试 token，除 root-only
  secret store 外必须零命中。

### HTTP 安全

- 精确 origin 成功；不同 host、port、scheme、Host override、绝对 URL 和 scheme-relative URL
  全部拒绝。
- HTTP 和证书错误 fail closed。
- 301/302/307/308 同域与跨域均不自动跟随，下一跳收不到 auth header。
- bearer/header/query 三种受限注入正确，agent 自带认证头被删除。
- 已存 secret 原值、敏感 JSON field 和响应 Cookie/auth headers 在返回 agent 前被打码。
- 日志 grep 不到 token 全值或固定测试片段。

### 集成与隔离

- `bx mcp install` 一次配对后，新增/轮换 Credential 无需改 agent 配置。
- keyd 未安装或停止时，Tool Keys 工具优雅降级，现有 bx MCP 诊断/控制工具仍可用。
- 强制杀掉 keyd、损坏 keyd 数据和模拟 keyd 升级失败，bx TUN、路由、DNS、传输、
  menu 网络状态与 core 控制面不受影响。
- macOS 真机 e2e：agent 提议 unknown HTTPS origin → BxMenu 只粘贴 token → keyd 代理
  请求 → 上游 200/401 返回 → agent 可调整 AuthHint，整个过程 agent env/context/log 无 token。

## 交付切片

V1 按以下顺序实现，每个切片都保持 bx core 可独立构建和运行：

1. keyd 数据模型、root-only 存储、Unix LocalAPI 和严格 origin-bound HTTP client。
2. MCP catalog/request/call 适配器和 token 零暴露回归测试。
3. BxMenu pending-request 安全粘贴、手动添加和管理页。
4. launchd 可选安装/卸载、独立升级和 macOS 真机 e2e。

HTTP Base URL 兼容代理、供应商 recipe、方法/路径策略、组织身份、团队共享、云同步、
Windows/Linux UI 均是后续独立设计，不作为 V1 完成的隐式前提。

## 决策记录

- 放弃「通用推断供应商权限」；401/403 是 agent 学习 API 的正常反馈。
- 放弃默认 method/path 权限表；不用低准确率规则制造虚假安全和复杂 onboarding。
- 默认 agent-assisted pairing；对 unknown provider，用户只粘贴 token 并确认已显示的
  hostname。
- origin 是唯一不能省略的通用限制；它把可外传秘密变成只能对指定服务使用的
  能力。
- Tool Keys 是 bx 的可选 companion service，不进 bx core 守护进程，不引入通用动态插件
  框架。
- V1 只面向个人 macOS + BxMenu + MCP，以最小闭环验证「密钥零接触 + 极简管理」。
