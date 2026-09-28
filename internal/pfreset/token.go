package pfreset

import (
	"context"
	"errors"
	"os"
	"strings"
)

// ParseEnableToken 从 `pfctl -E` 的输出里取 token(形如 `Token : 1234567890`)。
// 没有那一行就是失败 —— 拿一个编出来的 0 去 -X 会释放别人的引用。
func ParseEnableToken(out string) (string, error) {
	for _, line := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == "Token" {
			if tok := strings.TrimSpace(v); tok != "" {
				return tok, nil
			}
		}
	}
	return "", errors.New("pfctl -E printed no Token line")
}

// FlushStale 清掉上一个 Core 留下的东西:anchor 里有规则就冲掉,token 文件在就释放
// 并删掉。都没有就一个破坏性的 pfctl 都不调。run 是 pfctl 的执行者(生产 runPfctl,
// 测试用假的)。
func FlushStale(ctx context.Context, tokenPath string, run func(ctx context.Context, args ...string) (string, error)) (bool, error) {
	flushed := false
	var errs error
	if out, err := run(ctx, "-a", Anchor, "-s", "rules"); err == nil && strings.TrimSpace(out) != "" {
		flushed = true
		if _, err := run(ctx, "-a", Anchor, "-F", "all"); err != nil {
			errs = errors.Join(errs, err)
		}
	}
	if raw, err := os.ReadFile(tokenPath); err == nil {
		flushed = true
		if tok := strings.TrimSpace(string(raw)); tok != "" {
			if _, err := run(ctx, "-X", tok); err != nil {
				errs = errors.Join(errs, err)
			}
		}
		if err := os.Remove(tokenPath); err != nil {
			errs = errors.Join(errs, err)
		}
	}
	return flushed, errs
}
