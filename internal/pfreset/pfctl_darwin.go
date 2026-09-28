//go:build darwin

package pfreset

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type pfctlDriver struct{ tokenPath string }

// NewDriver 返回 pfctl 驱动。tokenPath 是 `-E` 拿到的引用 token 落盘处:Core 崩溃时
// 只有它能告诉下一个人「有一个引用要释放」。
func NewDriver(tokenPath string) Driver { return pfctlDriver{tokenPath: tokenPath} }

func runPfctl(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "/sbin/pfctl", args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("pfctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func (d pfctlDriver) Enable(ctx context.Context) (string, error) {
	out, err := runPfctl(ctx, "-E")
	if err != nil {
		return "", err
	}
	token, err := ParseEnableToken(out)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(d.tokenPath, []byte(token+"\n"), 0o600); err != nil {
		// 记不下来就不做:崩溃时没人知道要释放,与「不留下没人管的状态」同一条。
		_, _ = runPfctl(ctx, "-X", token)
		return "", fmt.Errorf("recording the pf token: %w", err)
	}
	return token, nil
}

func (d pfctlDriver) Load(ctx context.Context, rules string) error {
	cmd := exec.CommandContext(ctx, "/sbin/pfctl", "-a", Anchor, "-f", "-")
	cmd.Stdin = strings.NewReader(rules)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("pfctl -a %s -f -: %w: %s", Anchor, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (d pfctlDriver) Flush(ctx context.Context) error {
	return flushAnchor(ctx, runPfctl)
}

func (d pfctlDriver) Release(ctx context.Context, token string) error {
	_, err := runPfctl(ctx, "-X", token)
	_ = os.Remove(d.tokenPath)
	return err
}

// FlushStaleDarwin 是给 cli 强制拆除与 Guardian 用的入口(真 pfctl)。
func FlushStaleDarwin(ctx context.Context, tokenPath string) (bool, error) {
	return FlushStale(ctx, tokenPath, runPfctl)
}
