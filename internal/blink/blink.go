// Package blink 是 bx 对外的链接别名:把 brook 链接 base64url 换壳成 blink://,
// 对用户隐藏 brook/IP/密码明文。仅在 setup 入口解码回 brook,运行时不涉及。
package blink

import (
	"encoding/base64"
	"fmt"
	"strings"
)

const scheme = "blink://"

// Encode 把 brook 链接包成 blink://(不校验输入是否为 brook,调用方保证)。
func Encode(brookLink string) string {
	return scheme + base64.RawURLEncoding.EncodeToString([]byte(brookLink))
}

// Decode 还原 blink:// 为 brook 链接;校验 scheme、base64、内容前缀。
func Decode(s string) (string, error) {
	if !strings.HasPrefix(s, scheme) {
		return "", fmt.Errorf("不是 blink 链接(应以 %s 开头)", scheme)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, scheme))
	if err != nil {
		return "", fmt.Errorf("blink 解码失败: %w", err)
	}
	link := string(raw)
	if !strings.HasPrefix(link, "brook://") {
		return "", fmt.Errorf("blink 内容不是 brook 链接")
	}
	return link, nil
}
