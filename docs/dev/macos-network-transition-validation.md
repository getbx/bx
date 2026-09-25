# macOS Network Transition Validation

bx 在网络变化后自动安全恢复。恢复期间外网可能短暂不可用，但流量始终保持 fail-closed，绝不回落直连。

## Dry Run

默认只记录计划，不读取状态、不切换网络：

```bash
scripts/darwin-testkit.sh --network-transition-check \
  --bx /usr/local/bin/bx
```

脚本使用 `mktemp` 创建唯一、权限为 `0700` 的日志目录，并在输出中打印该路径。检查其中的 `plan.txt` 和 `user-sequence.txt`。脚本不会调用 Wi-Fi 控制命令，也不会调用 `bx reconnect`。

## User-Operated Check

真实验收必须由用户确认 bx 已经 healthy，并亲手完成网络切换：

```bash
scripts/darwin-testkit.sh --network-transition-check \
  --execute \
  --acknowledge-physical-change \
  --bx /usr/local/bin/bx
```

脚本先把 `bx status --json` 保存为 `before-status.json`，并拒绝非 `Protected` 或 tunnel unhealthy 的起点。随后用户在 macOS 中手动选择另一个 Wi-Fi、热点或其他物理网络，回到终端输入 `NETWORK-CHANGED`。脚本只轮询只读 status，记录 `recovery-timeline.ndjson` 和 `after-status.json`，并要求新的 `network_generation` 最终回到 `Protected`。

如需 `--log-dir`，必须传入尚不存在的唯一目录路径。其已存在父目录链必须是规范路径，不含符号链接，逐级由 root 或执行 sudo 的用户拥有，且任何一级都不可被 group/other 写入；因此自定义路径不能放在可写的 `/tmp` 链下。脚本验证父链后仅创建最终目录一次，并以限制性 umask 和 mode `0700` 创建。不要使用可预测的固定临时路径。

`bx reconnect` 只用于 troubleshooting，不属于正常网络切换流程。不得用本脚本或自动化调用 `networksetup`、切换 Wi-Fi、执行 `bx up/down/reconnect`，也不得为了验收临时关闭 fail-closed。

## Package Staging

包验证只允许 staging，不安装、不启动服务：

```bash
STAGING="$(mktemp -d)"
BX_VERSION=dev BX_RELEASE_DIR="$STAGING" scripts/package-macos-release.sh
BX_RELEASE_DIR="$STAGING" scripts/verify-macos-release.sh
```

验收目录应保留 `user-sequence.txt`、`user-confirmation.txt`、前后 status snapshots 和 recovery timeline。任何真实网络切换都必须由用户在明确授权后手动完成。

## Private Listener Boundary

Core 为外部传输进程选择私有辅助监听端口时，会显式排除配置的固定监听端口并限制分配尝试次数。由于端口必须交给外部进程，未采用文件描述符继承时，关闭探测 socket 到子进程绑定之间存在不可消除的 TOCTOU 窗口。bx 不宣称该交接是原子的；冲突会使候选传输无法通过启动/健康门，并保持 fail-closed，不能触发 direct fallback。
