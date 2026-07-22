# macOS 统一 Bx.app 设计

Status: APPROVED-FOR-SPEC-REVIEW (2026-07-20)

## 背景

bx 当前的 macOS 完整包会同时安装 `Bx.app` 与 `/usr/local/bin/bx`，更新也已经能够一次
替换两者。但产品运行时仍有两个独立真相源：BxMenu 硬编码调用系统 CLI，CLI 自行安装
root Core 与 launchd 服务，菜单栏由另一个用户 LaunchAgent 拉起。这使用户可能遇到：

- Finder 中出现多个开发版或旧版 `Bx.app`；
- 新菜单栏配旧 CLI，或新 CLI 配旧菜单栏；
- 两个 LaunchAgent 同时拉起两个菜单图标；
- 打开 App、执行 `bx up`、更新和退出分别表现为互不相关的动作；
- 普通用户不知道应该安装 App、安装 CLI，还是两者都做。

macOS 产品必须收敛成一个可理解的对象：用户安装并打开 **Bx.app**。菜单栏、命令行与
root 网络服务仍保持权限隔离，但由同一 App 包、同一版本和同一更新事务管理。

本设计补充 `2026-07-16-macos-guardian-lifecycle-update-design.md`。Guardian 继续作为
root Core 的唯一生命周期所有者；本文定义 App、CLI bridge、Guardian/Core 资产的统一
安装和产品体验，不改变数据面或 Guardian 的 fail-closed 更新承诺。

## 产品承诺

1. macOS 用户只下载、安装和打开一个 `/Applications/Bx.app`。
2. 打开 Bx.app 后只出现一个菜单栏实例；重复打开只唤醒现有实例。
3. 首次运行通过一次清晰的管理员授权安装命令行入口和 root Core 服务。
4. `bx` 命令始终指向当前 Bx.app 所属版本，不再形成第二条独立升级链路。
5. App、CLI bridge、Guardian 与 Core 使用同一个 release version 和 build identity。
6. 更新是一个完整事务；成功后组件版本一致，失败则完整回滚，不留下混合版本。
7. 菜单栏退出不停止保护；**Turn Off bx** 才停止 Core 并恢复由 bx 管理的网络状态。
8. App UI 崩溃、退出或更新失败不得中断健康的 Core。
9. 安装、迁移和更新不能静默启动保护或修改用户的连接配置。
10. root 服务不得执行位于用户可写目录中的二进制。

## 非目标

- 不把 TUN、路由、DNS 或传输逻辑移入 Swift GUI 进程。
- 不让整个 Bx.app 以 root 身份运行。
- 不删除仓库构建产物或用户任意目录中名称相同但身份无法证明的 App。
- 不在无 Apple 签名阶段伪装成已具备完整的 SMAppService/Network Extension 信任链。
- 不取消 CLI、MCP 或自动化接口；它们是统一产品的正式表面，不是另一款产品。

## 统一架构

```text
/Applications/Bx.app                         唯一用户产品与版本真相源
├── Contents/MacOS/Bx                        菜单栏、设置、授权与状态 UI
├── Contents/Resources/bx-cli                该 release 的 CLI 资产
├── Contents/Library/LaunchServices/
│   └── bx-installer                         未来签名 privileged helper
└── Contents/Resources/release.json          版本、digest、兼容协议

/usr/local/bin/bx                            root-owned CLI bridge
/Library/Application Support/bx/runtime/     root-owned 已激活运行时
├── <version>/bx                             Guardian/Core executable
├── <version>/release.json
└── current -> <version>

com.getbx.bx.guard                           root Guardian LaunchDaemon
com.getbx.bx.menu                            用户登录项，仅负责打开 Bx.app
```

### Bx.app

Bx.app 是唯一面向用户的安装对象。它负责：

- 显示菜单栏、状态、首次设置、诊断和更新授权；
- 读取 bundle 内版本与 Guardian 报告的运行时版本；
- 请求安装或更新统一 runtime；
- 调用 Guardian LocalAPI 执行启动、停止、重连和安全更新；
- 向用户解释需要授权、更新完成、已回滚或需要诊断。

App 不直接编辑路由/DNS，不直接启动 Core 子进程，不读取 client link 明文，不通过 shell
拼接管理员命令。一个用户会话只允许一个 App 实例；第二次打开通过 bundle identifier
激活现有实例。

### CLI bridge

`/usr/local/bin/bx` 是小型、root-owned、稳定的命令入口，不是独立产品。它负责：

1. 定位 `/Library/Application Support/bx/runtime/current`；
2. 验证 runtime metadata 与文件所有权/权限；
3. 执行同版本 CLI；
4. runtime 缺失或损坏时给出打开 Bx.app 修复的明确提示。

bridge 不从 `~/Applications`、下载目录或其他用户可写路径加载代码。普通只读命令可直接
调用 runtime CLI；需要 root 的命令继续由 CLI/Guardian 的既有鉴权边界处理。agent 与
MCP 始终使用 `/usr/local/bin/bx`，因此不需要知道 App bundle 内部路径。

开发模式仍允许仓库中的 `./bx` 独立运行测试，但开发二进制不得注册为生产 LaunchDaemon
或覆盖统一 App runtime，除非用户显式执行开发安装流程。

### Guardian 与 Core runtime

Guardian/Core 的可执行资产从经过验证的 Bx.app release 安装到 root-owned runtime 目录，
而不是直接从用户拥有的 App bundle 以 root 权限执行。这样即使 App 位于可写目录、被移动
或 UI 更新中断，root 服务也不会加载未经授权替换的代码。

Guardian 继续遵循已有设计：它是 Core 的唯一父进程、持有 desired state、执行
maintenance barrier、安全激活和回滚。统一 App 不改变这些不变量。

## 用户流程

### 安装与首次打开

1. 用户把唯一的 Bx.app 放入 `/Applications` 并打开。
2. App 检查 bundle 位置、runtime、CLI bridge、Guardian 和历史安装。
3. 未安装 runtime 时显示一个主要动作 **Install bx**。
4. 用户确认后，macOS 请求一次管理员授权。
5. 安装器验证 App 内 release metadata、文件 digest、架构和版本，然后原子安装 runtime、
   CLI bridge 与 Guardian。
6. App 注册一个用户级登录项，使菜单栏在登录后出现。
7. 若已有配置则显示当前 Off/Protected 状态；没有配置则显示 **Set Up bx**。
8. 安装本身不执行 `bx up`。设置成功后才询问是否开启保护。

安装完成后，用户无需理解 CLI 和 Core 的区别。终端中的 `bx --version` 与 App 的版本必须
一致；Guardian 运行的是同一 release 的 runtime。

### 日常启动

- 打开 Bx.app：显示或唤醒菜单栏，不重复启动 Core。
- `sudo bx up`：通过 Guardian 开启保护，并 best-effort 唤醒已安装的 Bx.app。
- 登录 macOS：登录项启动 Bx.app；Guardian 根据 desired state 决定是否恢复 Core。
- Quit Menu：只退出 App；Core 和保护继续运行。
- Turn Off bx：经 Guardian 停止 Core、恢复网络，再保持 App 打开并显示 Off。
- Quit bx：先明确确认 Turn Off，成功后退出 App；停止失败时 App 保持打开并提供 Doctor。

### 更新

App 与 CLI 使用同一签名 manifest 和完整 macOS package。更新事务分为：

1. 在旧 Core 继续保护时下载并验证完整 release；
2. 验证 App、CLI bridge/runtime、Guardian/Core 和 metadata 版本一致；
3. 建立 App 与 root runtime 的统一回滚快照；
4. 由 Guardian 按既有 maintenance barrier 设计激活新 Core；
5. 新 Core 通过完整健康门后切换 `runtime/current`；
6. 原子替换 Bx.app，重新唤醒菜单栏；
7. 写入一个覆盖所有组件的 receipt。

任何阶段失败都不得留下“新 App + 旧 bridge + 半个新 Core”。若新 Core 无法健康启动，
Guardian 恢复旧 runtime；App 同时恢复旧 bundle 或显示仍在运行的旧版本。保护运行时允许
短暂无法联网，但不能回落真实公网直连。

## 单实例与旧副本迁移

统一安装器只认 `/Applications/Bx.app` 为生产路径，并清理已知历史安装：

- 停止并删除 `com.ggshr9.bx.menu`；
- 幂等重建唯一的 `com.getbx.bx.menu` 登录项；
- 识别 `~/Applications/Bx.app` 以及旧安装器记录中的已知路径；
- 只有当 bundle identifier、签名/开发期 digest、版本 metadata 都能证明它是 bx 管理的
  旧副本，且其进程不是当前唯一运行实例时，才移动到废纸篓或删除；
- `dist/`、`.build/`、worktree 和任意用户目录中的开发产物不自动删除，只从 Spotlight
  生产安装范围和登录项中排除；
- 迁移完成后再次枚举 bx menu 进程，确保只剩一个 bundle path 与一个 LaunchAgent label。

如果当前运行的是待迁移旧副本，安装器先启动 `/Applications/Bx.app` 并完成握手，再退出
旧实例，避免菜单栏空窗。菜单实例切换不触碰 Core 或网络。

## 无签名阶段与签名终态

### 当前阶段

在尚无 Apple Developer ID 时，完整 tar package 继续使用 bx 自己的 Ed25519 manifest 与
SHA-256 验证。用户从包内运行一次安装器，安装器通过 `sudo` 把 runtime 与 bridge 写入
root-owned 路径，并把 Bx.app 安装到 `/Applications`。这已经能提供统一产品体验，但
Gatekeeper 仍会显示未公证提示。

### 签名阶段

获得 Developer ID 后：

- Bx.app、CLI/runtime、installer helper 使用同一 Team ID 和 designated requirement；
- App 启用 hardened runtime、Developer ID 签名与 notarization；
- 使用 Apple 支持的 privileged helper/SMAppService 管理 root Guardian；
- 使用登录项 API 取代手写用户 LaunchAgent；
- updater 同时验证 Apple code requirement 和 bx release manifest；
- Team ID 与产品签名身份成为旧副本识别和授权的重要依据。

签名只替换信任与授权实现，不改变本文的用户流程、路径所有权和版本事务模型。

## 状态与错误处理

App 启动时组合三个版本：

- `bundle_version`：当前 Bx.app；
- `installed_runtime_version`：`runtime/current`；
- `running_core_version`：Guardian 实际监督的 Core。

正常状态要求三者一致。允许的短暂状态只有正在执行统一更新事务；其他不一致都显示
**Repair bx**，而不是继续让菜单调用未知版本 CLI。

错误处理原则：

- App 缺失：CLI/Core 继续工作，`bx status` 提示重新安装 App，不停止保护；
- CLI bridge 缺失：App 可授权修复，不重装配置、不重启健康 Core；
- Guardian 不可达：App 显示 Needs Attention，只允许 Doctor/Repair，不自行 shell fallback；
- Core 不健康：Guardian fail closed，App 显示红色并提供 Doctor；
- 菜单重复：保留 `/Applications/Bx.app` 实例，安全退出已知旧实例，不影响 Core；
- 更新混合版本：保持或恢复最后完整 release，不将部分成功报告为已更新。

## 安全边界

1. root-owned bridge/runtime 目录不可由普通用户写入。
2. privileged installer 只接受当前 Bx.app 内经过 release 验证的固定资产，不接受任意路径。
3. App 到 Guardian 的 mutating API 继续使用 peer credential/授权机制，不靠 socket 可写权限。
4. 所有 argv、日志、receipt 和 metadata 不包含 client link、token 或服务密码。
5. App 被移动、替换或退出不能改变 Guardian desired state。
6. 更新与 Repair 都复用同一事务引擎，不另造不具备回滚能力的复制脚本。
7. 不允许 `/usr/local/bin/bx` 软链接到用户可写的 `~/Applications/Bx.app`。

## 测试与验收

### 自动测试

1. CLI bridge：正确 runtime、缺失、权限错误、metadata 不匹配和路径逃逸。
2. release 一致性：App/bridge/Core 版本或 digest 任一不一致都拒绝安装。
3. 安装事务：全新安装、幂等重装、部分复制失败、回滚和配置保留。
4. 单实例：重复打开、登录项重复、legacy label 与两个已知 App 副本迁移。
5. 更新：Off、Protected、激活成功、新 Core 失败回滚、App 替换失败回滚。
6. UI：Install、Set Up、Protected、Off、Repair、Updating 与 Needs Attention。
7. CLI/agent：`bx --version`、status、check、MCP 在统一 runtime 下保持兼容。
8. 安全：拒绝用户可写 runtime、恶意 archive path、错误 owner/mode 和未验证 helper 请求。

### macOS 真机验收

真机网络测试仍遵循仓库规则：开发 agent 不得自行执行 `bx up/down` 或修改路由；涉及网络
的步骤先 dry-run，再由用户明确授权。

验收矩阵：

- 干净 Mac 只安装 Bx.app，首次授权后 App、CLI、Guardian/Core 版本一致；
- 已有 `/usr/local/bin/bx`、`~/Applications/Bx.app` 和 legacy LaunchAgent 的机器可幂等迁移；
- 打开 App 十次仍只有一个菜单图标和一个 App 进程；
- `sudo bx up` 在已有登录用户时唤醒菜单栏，但不创建第二实例；
- Quit Menu 后保护持续，重新打开 App 恢复状态；
- 运行中完整更新成功时短暂断网且不直连，新版本实际激活；
- 注入 Core/App/bridge 替换失败时完整回滚；
- 删除 App 后 Core 继续保护，重新安装 App 可无损修复；
- Finder/Spotlight 的生产安装范围只剩 `/Applications/Bx.app`。

## 分阶段交付

### Phase 1：统一无签名产品包

- 调整 Bx.app bundle layout，内含版本化 CLI/runtime 资产；
- 新增 root-owned runtime 与稳定 CLI bridge；
- 将完整包安装目标统一到 `/Applications/Bx.app`；
- 合并菜单安装、CLI 安装和 legacy 清理；
- 加入单实例与受控旧副本迁移；
- 更新 README、Help、release 验证和诊断输出。

### Phase 2：统一更新与 Repair

- 让 Guardian update transaction 同时覆盖 App、bridge 与 runtime；
- 增加三版本一致性状态和 Repair；
- 菜单与 CLI 共享同一更新进度和 receipt；
- 完成失败注入与 macOS 真机验收。

### Phase 3：Apple 原生签名与服务管理

- Developer ID、hardened runtime、notarization；
- privileged helper/SMAppService 与登录项 API；
- Apple code requirement + bx manifest 双重验证；
- 为未来 Network Extension 保留 bundle/service 边界。

## 成功标准

普通用户对 macOS bx 的理解只剩一句：**安装并打开 Bx.app**。终端与 agent 仍可自然使用
`bx`，但 App、CLI 和网络 Core 不再拥有独立版本或独立安装生命周期。任何更新、修复、
迁移和退出操作都保持权限隔离、版本一致与现有 fail-closed 网络安全承诺。
