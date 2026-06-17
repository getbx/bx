// Package blink 是 bx 对外的链接别名:把内部传输链接 base64url 换壳成 bx://。
// 旧 blink:// 仍可解码,用于兼容已发出的早期链接。
package blink

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	scheme       = "bx://"
	legacyScheme = "blink://"
)

type envelope struct {
	Version   int    `json:"v"`
	Transport string `json:"transport"`
	Link      string `json:"link"`
}

// Encode 把内部传输链接包成 bx://。
func Encode(link string) string {
	e := envelope{Version: 1, Transport: "brook", Link: link}
	b, _ := json.Marshal(e)
	return scheme + base64.RawURLEncoding.EncodeToString(b)
}

// Decode 还原 bx:// 或旧 blink:// 为内部传输链接;校验 scheme、base64、内容前缀。
func Decode(s string) (string, error) {
	prefix := scheme
	if strings.HasPrefix(s, legacyScheme) {
		prefix = legacyScheme
	} else if !strings.HasPrefix(s, scheme) {
		return "", fmt.Errorf("不是 bx 链接(应以 %s 开头)", scheme)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, prefix))
	if err != nil {
		return "", fmt.Errorf("bx 链接解码失败: %w", err)
	}
	link := string(raw)
	if strings.HasPrefix(strings.TrimSpace(link), "{") {
		var e envelope
		if err := json.Unmarshal(raw, &e); err != nil {
			return "", fmt.Errorf("bx 链接解析失败: %w", err)
		}
		if e.Version != 1 {
			return "", fmt.Errorf("不支持的 bx 链接版本: %d", e.Version)
		}
		if e.Transport != "brook" {
			return "", fmt.Errorf("不支持的 bx transport: %s", e.Transport)
		}
		link = e.Link
	}
	if !strings.HasPrefix(link, "brook://") {
		return "", fmt.Errorf("bx 链接内容不受支持")
	}
	return link, nil
}
