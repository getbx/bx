package deploy

import "strings"

// Classify 把一次失败归到用户能据以行动的那一类。菜单按类出话,原文放进「详情」。
func Classify(text string) string {
	t := strings.ToLower(text)
	has := func(s ...string) bool {
		for _, x := range s {
			if strings.Contains(t, x) {
				return true
			}
		}
		return false
	}
	switch {
	case has("remote host identification has changed", "host key verification failed"):
		return "host_key_changed"
	case has("password has expired", "password change required", "you are required to change your password"):
		return "password_change_required"
	case has("permission denied", "too many authentication failures", "unable to authenticate"):
		return "auth_failed"
	case has("timed out", "connection refused", "no route to host", "could not resolve hostname", "network is unreachable", "i/o timeout", "no such host", "connection closed by", "connection reset"):
		return "unreachable"
	case has("sudo: a password is required", "sudo: a terminal is required", "incorrect password attempt"):
		return "sudo_password"
	case has("architecture was not recognized", "systemctl: command not found", "systemctl: not found"):
		return "unsupported_system"
	case has("checksum mismatch", "checksum of"):
		return "checksum"
	case has("is already used by"):
		// 端口被别的程序占着(网站、别家代理)。什么都没改。
		return "port_in_use"
	case has("already exists (pass --force", "already has bx, but"):
		// 这台上已经装过 bx server。菜单据此问「重装(换新钥匙)吗」,而不是报一句失败。
		return "already_installed"
	}
	return "install_failed"
}
