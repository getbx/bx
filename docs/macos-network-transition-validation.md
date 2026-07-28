# macOS Network Transition Validation

bx 在网络变化后自动安全恢复。恢复期间外网可能短暂不可用，但流量始终保持 fail-closed，绝不回落直连。

## Dry Run

默认只记录计划，不读取状态、不切换网络：

```bash
scripts/darwin-testkit.sh --network-transition-check \
  --log-dir /tmp/bx-network-transition-check
```

检查 `plan.txt` 和 `user-sequence.txt`。脚本不会调用 Wi-Fi 控制命令，也不会调用 `bx reconnect`。

## User-Operated Check

真实验收必须由用户确认 bx 已经 healthy，并亲手完成网络切换：

```bash
scripts/darwin-testkit.sh --network-transition-check \
  --execute \
  --acknowledge-physical-change \
  --bx /usr/local/bin/bx \
  --log-dir /tmp/bx-network-transition-check
```

脚本先把 `bx status --json` 保存为 `before-status.json`，并拒绝非 `Protected` 或 tunnel unhealthy 的起点。随后用户在 macOS 中手动选择另一个 Wi-Fi、热点或其他物理网络，回到终端输入 `NETWORK-CHANGED`。脚本只轮询只读 status，记录 `recovery-timeline.ndjson` 和 `after-status.json`，并要求新的 `network_generation` 最终回到 `Protected`。

`bx reconnect` 只用于 troubleshooting，不属于正常网络切换流程。不得用本脚本或自动化调用 `networksetup`、切换 Wi-Fi、执行 `bx up/down/reconnect`，也不得为了验收临时关闭 fail-closed。

## Package Staging

包验证只允许 staging，不安装、不启动服务：

```bash
STAGING="$(mktemp -d)"
BX_VERSION=dev BX_RELEASE_DIR="$STAGING" scripts/package-macos-release.sh
BX_RELEASE_DIR="$STAGING" scripts/verify-macos-release.sh
```

验收目录应保留 `user-sequence.txt`、`user-confirmation.txt`、前后 status snapshots 和 recovery timeline。任何真实网络切换都必须由用户在明确授权后手动完成。
