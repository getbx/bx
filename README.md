# bx

bx 是一个开箱即用的透明全局代理。服务端和客户端都只需要一个 `bx` 二进制:

- VPS 上运行 `bx server`
- 本机运行 `bx setup` / `bx up`
- 客户端只使用 `bx://` 链接

应用无需配置代理。bx 会在网络层接管流量,自动分流、处理 DNS,并在隧道不可用时 fail-closed,避免真实 IP 裸奔。

## 快速开始

### 0. 下载 bx

从 GitHub Releases 下载对应平台的压缩包:

```bash
# Linux x86_64 示例
curl -LO https://github.com/getbx/bx/releases/latest/download/bx_linux_amd64.tar.gz
tar -xzf bx_linux_amd64.tar.gz
chmod +x bx
./bx --version
```

可选校验:

```bash
curl -LO https://github.com/getbx/bx/releases/latest/download/SHA256SUMS
sha256sum -c SHA256SUMS --ignore-missing
```

### 1. VPS 安装 bx server

```bash
sudo ./bx server install --host <VPS_IP或域名>
sudo bx server start
```

`server install` 会自动生成密码、写入 `/etc/bx/server.yaml`、安装 systemd 服务,并在传入 `--host` 时打印客户端 `bx://` 链接。

之后也可以随时重新生成链接:

```bash
sudo bx server link --host <VPS_IP或域名>
```

### 2. 客户端安装 bx

```bash
sudo ./bx setup bx://...
sudo bx up
```

日常使用:

```bash
bx status
sudo bx down
sudo bx up
```

## 命令

| 命令 | 作用 |
|---|---|
| `sudo bx server install --host <host>` | VPS 首次安装 bx server |
| `sudo bx server start` | 启动 bx server 并设为开机自启 |
| `sudo bx server stop` | 停止 bx server 并取消开机自启 |
| `sudo bx server link --host <host>` | 生成客户端 `bx://` 链接 |
| `sudo bx server uninstall` | 卸载 bx server 服务 |
| `sudo bx setup bx://...` | 客户端首次配置 |
| `sudo bx up` | 启动客户端并设为开机自启 |
| `sudo bx down` | 停止客户端并取消开机自启 |
| `bx status` | 查看客户端状态面板 |
| `sudo bx run` | 前台运行,用于调试 |
| `sudo bx uninstall` | 卸载客户端服务 |

## 配置

客户端默认配置路径:

```text
/etc/bx/config.yaml
```

服务端默认配置路径:

```text
/etc/bx/server.yaml
```

通常不需要手写配置。`bx server install` 和 `bx setup` 会自动生成需要的文件。

客户端支持的常用配置:

```yaml
server: "bx://..."
killswitch: true
global: false
dns:
  china: 223.5.5.5
  fakeip_cidr: 198.18.0.0/15
bypass:
  - 10.0.0.0/16
rules:
  - direct: ["*.corp.internal", "10.0.0.0/8"]
  - proxy: ["*.openai.com"]
```

说明:

- `killswitch: true`:隧道不健康时阻断代理连接。
- `global: true`:除内网和用户直连规则外,所有流量都走 bx 隧道。
- `bypass`:路由层绕过 bx 的网段,适合管理网、SSH、内网。
- 私网、Docker、loopback、link-local 默认内建直连,通常无需手动配置。

## 构建

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o bx .
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o bx .
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -o bx .
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -o bx .
```

运行时需要 root 权限配置 TUN 和系统路由。

## 测试

```bash
go test ./...
```

端到端测试需要在真实机器上以 root 运行。
