// Package blink 是 bx 对外的链接别名:把内部传输链接 base64url 换壳成 bx://。
// 旧 blink:// 仍可解码,用于兼容已发出的早期链接。
package blink

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/getbx/bx/internal/tunnel"
)

const (
	scheme       = "bx://"
	legacyScheme = "blink://"
)

type envelope struct {
	Version   int      `json:"v"`
	Transport string   `json:"transport,omitempty"` // 单传输(legacy)
	Link      string   `json:"link,omitempty"`      // 单传输(legacy)
	Links     []string `json:"links,omitempty"`     // 多传输 bundle(有序优先级)
}

// transportOf 由链接 scheme 推传输标识。委托 tunnel.Kind(唯一真相源,与 supervisor/setup 同源)。
func transportOf(link string) string { return tunnel.Kind(link) }

// supportedLink 报告链接内容是否为受支持的裸传输链接。委托 tunnel.IsClientLink(单一识别口径)。
func supportedLink(link string) bool { return tunnel.IsClientLink(link) }

// Encode 把单个内部传输链接(brook:// 或 vless://)包成 bx://(legacy 单格式)。
func Encode(link string) string {
	e := envelope{Version: 1, Transport: transportOf(link), Link: link}
	b, _ := json.Marshal(e)
	return scheme + base64.RawURLEncoding.EncodeToString(b)
}

// EncodeMulti 把有序多传输(优先级,主在前)打包成单条 bx://,供「一贴配好全部+容灾」。
// 单元素退化为 legacy 单格式(向后兼容旧 Decode);0 元素返回空串。
func EncodeMulti(links []string) string {
	switch len(links) {
	case 0:
		return ""
	case 1:
		return Encode(links[0])
	}
	e := envelope{Version: 1, Links: links}
	b, _ := json.Marshal(e)
	return scheme + base64.RawURLEncoding.EncodeToString(b)
}

// Decode 还原 bx:// 或旧 blink:// 为内部传输链接;bundle 返回首条(主传输)。
func Decode(s string) (string, error) {
	all, err := DecodeAll(s)
	if err != nil {
		return "", err
	}
	return all[0], nil
}

// DecodeAll 还原 bx:// 或旧 blink:// 为有序传输列表:单格式/legacy → 1 元素;bundle → N 元素。
// 校验 scheme、base64、版本与每条内容前缀(内容闸不放松:仅 brook/vless)。
func DecodeAll(s string) ([]string, error) {
	prefix := scheme
	if strings.HasPrefix(s, legacyScheme) {
		prefix = legacyScheme
	} else if !strings.HasPrefix(s, scheme) {
		return nil, fmt.Errorf("不是 bx 链接(应以 %s 开头)", scheme)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, prefix))
	if err != nil {
		return nil, fmt.Errorf("bx 链接解码失败: %w", err)
	}
	var links []string
	if strings.HasPrefix(strings.TrimSpace(string(raw)), "{") {
		var e envelope
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil, fmt.Errorf("bx 链接解析失败: %w", err)
		}
		if e.Version != 1 {
			return nil, fmt.Errorf("不支持的 bx 链接版本: %d", e.Version)
		}
		if len(e.Links) > 0 {
			links = e.Links // 多传输 bundle
		} else {
			if e.Transport != "" && e.Transport != "brook" && e.Transport != "reality" && e.Transport != "hysteria2" && e.Transport != "trojan" && e.Transport != "shadowsocks" && e.Transport != "vmess" {
				return nil, fmt.Errorf("不支持的 bx transport: %s", e.Transport)
			}
			links = []string{e.Link} // legacy 单格式
		}
	} else {
		links = []string{string(raw)} // 更早的 legacy 裸 base64
	}
	if len(links) == 0 {
		return nil, fmt.Errorf("bx 链接为空")
	}
	for i, link := range links {
		if !supportedLink(link) {
			return nil, fmt.Errorf("bx 链接内容不受支持(第 %d 条)", i)
		}
	}
	return links, nil
}
