package cli

import (
	"encoding/base64"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 仓库里不许出现**能用的**服务器链接。
//
// 上一条(地址那条)只拦 IP。而真正不能有的东西是**凭据**:`vless://` 的 uuid、
// trojan/hysteria2 的密码、`bx://` 那个 base64 信封里包着的同样东西 —— 一条真链接
// 进了公开仓库,别人不是"知道你有台服务器",是**可以直接用它**。
//
// 最可能的事故形状不是手打,是**粘贴**:`bx server share` / `bx blink` 打出来的那一
// 行,顺手贴进一个 issue、一份 spec、一条测试。所以判据落在「凭据位长得像不像真的」
// 上,而不是「这一行在哪个文件里」。
//
// 2026-09-18 首次全量扫描抓到两个:`be625ca6-947d-46f4-8567-4bdcc5fd530d` 与
// `f293d747-66b5-4b8a-b102-6b1d981e5c97`,都在测试里、都配着占位主机。**它们是不是
// 当初某台真机的 uuid,我判断不出来** —— 而"判断不出"正是这条守卫要消灭的状态:
// 已按仓库既有的合成约定换掉,此后由判据保证这个问题不再出现。
//
// **它盖不住的**:`ss://` / `vmess://` / `brook://` 那几种把凭据编进 base64 的形式
// (解出来才认得,本条只解 `bx://`,因为那是 bx 自己 share 出来的格式、也是最可能被
// 粘贴的那一个)。别把它读成"凭据这件事已经全有人管了"。
func TestNoUsableServerLinksInTheRepo(t *testing.T) {
	out, err := exec.Command("git", "-C", filepath.Join("..", ".."), "ls-files").Output()
	if err != nil {
		t.Fatalf("列不出仓库文件:%v —— 这条守卫读不懂现在的仓库了,先修它", err)
	}
	files := strings.Fields(string(out))
	if len(files) == 0 {
		t.Fatal("一个文件都没扫到 —— 安静地扫了零个的守卫,与没有这条守卫完全一样")
	}

	scanned, links := 0, 0
	for _, rel := range files {
		if strings.HasPrefix(rel, "internal/embedded/assets/") {
			continue
		}
		body, readErr := readRepoTextFile(filepath.Join("..", "..", rel))
		if readErr != nil {
			continue
		}
		scanned++
		for _, secret := range credentialsInText(body) {
			links++
			if !secretLooksSynthetic(secret) {
				t.Errorf("%s 里有一条**可能真的能用**的链接,凭据位是 %q。\n"+
					"  一条真链接进公开仓库,别人不是「知道你有台服务器」,是可以直接用它。\n"+
					"  例子一律用合成值(uuid 写成 11111111-2222-3333-4444-555555555555,"+
					"密码写成 password / secret-… 之类)。", rel, secret)
			}
		}
	}
	if scanned == 0 {
		t.Fatal("一个文本文件都没读进来 —— 判据认不出这个仓库的形状了")
	}
	// 一条链接都没扫到,说明正则再也认不出仓库里那些例子了 —— 那时这条守卫
	// 恒绿而什么都不守,与没有它完全一样。
	if links == 0 {
		t.Fatal("一条带凭据的链接都没认出来 —— 判据漂了(仓库里确实有几十条示例链接)")
	}
}

var (
	// userinfo 式:scheme://<凭据>@host。
	userinfoLink = regexp.MustCompile(`\b(?:vless|trojan|hysteria2|hy2)://([^@/"'` + "`" + ` \t\n\\]{1,120})@`)
	// bx:// 是 bx 自己的信封(base64url 的 JSON),里面包着真链接。
	bxEnvelope = regexp.MustCompile(`\bbx://([A-Za-z0-9_-]{16,})`)
)

// credentialsInText 取出这段文字里每一条链接的凭据位。
func credentialsInText(body string) []string {
	var out []string
	for _, m := range userinfoLink.FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	// bx:// 解一层再递归一次 —— `bx server share` 打出来的正是这个形状,
	// 而它是最可能被整行粘贴的那一个。
	for _, m := range bxEnvelope.FindAllStringSubmatch(body, -1) {
		raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(m[1], "="))
		if err != nil {
			continue
		}
		var envelope struct {
			Link  string   `json:"link"`
			Links []string `json:"links"`
		}
		if json.Unmarshal(raw, &envelope) != nil {
			continue
		}
		for _, inner := range append(envelope.Links, envelope.Link) {
			for _, mm := range userinfoLink.FindAllStringSubmatch(inner, -1) {
				out = append(out, mm[1])
			}
		}
	}
	return out
}

// secretLooksSynthetic:这个凭据位一眼看得出是编的吗?
//
// **判据是「看得出是假的」,不是「看起来像真的」** —— 方向刻意如此:一个新的、
// 谁也没见过的真 uuid 长什么样,这里无从知道;而"编的"有明显特征(说人话的词、
// 重复的十六进制组、尖括号占位)。认不出就红,由人回来把它改成合成值或说明理由。
func secretLooksSynthetic(secret string) bool {
	lower := strings.ToLower(secret)
	// **太短的不可能是凭据。** bx 自己生成的是 uuid(36 字符)与长随机密码;
	// 一个 1~11 字符的串是例子,不是别人能用的东西。
	// **刻意接受的缺口**:一个人手选的短密码会从这里过去 —— 但那不是 bx 会生成的
	// 形状,而把阈值压到零会让仓库里几十条 `vless://uid@…` 这类示例全部变成红,
	// 而一条会误报的闸门比没有闸门更糟。
	if len(secret) < 12 {
		return true
	}
	// 说人话的占位:uuid / password / secret / user / example / test / old / new /
	// placeholder / <…> / … 。
	for _, word := range []string{
		"uuid", "password", "secret", "user", "example", "test", "placeholder",
		"old", "new", "your", "…", "<", ">", "xxx", "aaa", "foo", "bar",
	} {
		if strings.Contains(lower, word) {
			return true
		}
	}
	// 重复数字组成的 uuid/短 id:11111111-2222-…、22222222-…、11111111。
	stripped := strings.ReplaceAll(lower, "-", "")
	if stripped != "" && strings.Trim(stripped, "0123456789abcdef") == "" {
		for _, group := range strings.Split(lower, "-") {
			if group == "" {
				return false
			}
			// 每一组都必须是同一个字符重复 —— 真 uuid 不会长这样。
			if strings.Trim(group, string(group[0])) != "" {
				return false
			}
		}
		return true
	}
	return false
}
