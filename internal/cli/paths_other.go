//go:build !windows

package cli

// linux/darwin:客户端配置默认 /etc/bx/config.yaml。
const defaultConfigPath = "/etc/bx/config.yaml"
