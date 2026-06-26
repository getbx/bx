# 集成测试(netns,需 root)— 本地 Colima 跑法

bx 的门控集成测试(`//go:build integration`,如 netns 路由往返 PoC)需要 Linux + root +
网络命名空间。macOS 上跑不了,但**不必有物理 Linux 机**——用 Colima 起一个 Linux VM 即可。
所有路由改动只发生在测试自建的临时 netns 内,**不碰宿主、不碰你的 VPN/TUN**。

## 一次性准备
```sh
brew install colima docker   # 若未装
colima start                 # 起一个 Linux VM(独立网络,不劫持你的默认路由/VPN)
```

## 跑集成测试
```sh
# 在仓库根目录;Colima 默认把当前目录挂进 VM
colima ssh -- 'cd /Users/<你>/path/to/bx && sudo "$(which go)" test -tags integration ./... -v'
```
或进 VM 手动跑:
```sh
colima ssh
cd <repo>
sudo "$(which go)" test -tags integration ./internal/supervisor/ -run NetConfRoundTrip -v
```

## 能验什么 / 不能验什么(重要)
- **能验**:不发真实外网包的逻辑(路由规则装/拆、分流决策)。宿主是否挂 VPN 与结果无关。
- **不能验**:真实出口 IP / 泄漏审计。Colima VM 的真实出网经宿主 Mac;**宿主挂 VPN 时出口已被污染**,
  VM 里"测出口=VPS"不可信。真实泄漏审计**只能在真机(Mudi)的干净 WAN 上做**(见
  `docs/superpowers/specs/2026-06-25-task9-validation-harness-design.md`)。

## CI
GitHub Actions 的 `integration` job(`.github/workflows/ci.yml`)每次 push 在 ubuntu runner 上
以 root 自动跑这些测试,通常无需本地手跑。
