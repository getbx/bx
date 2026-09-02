package cli

import (
	"strings"
	"testing"
)

// 一张扫不出来的二维码与没有这个功能完全一样,而**在终端里它看起来是好的** ——
// 所以这几条钉的都是「扫描器要的那些性质」,不是「画出来了」。

// 一个 3×3 的对角线,手算得出每一格该是什么。
func diagonal(x, y int) bool { return x == y }

// **quiet zone 不是装饰,是规范的一部分**:四周必须留 4 个模块的空白,
// 少了它扫描器可能定位不到 —— 终端里尤其要紧,上一行提示文字会紧贴着码。
func TestQRKeepsTheQuietZone(t *testing.T) {
	out := qrHalfBlocks(3, diagonal, false)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")

	// 3 + 4*2 = 11 行模块,半块每行吃两行 ⇒ 6 行。
	if len(lines) != 6 {
		t.Fatalf("行数 %d,期望 6(3 模块 + 上下各 4 的 quiet zone,半块折半)", len(lines))
	}
	if got := len([]rune(lines[0])); got != 11 {
		t.Fatalf("列数 %d,期望 11(3 模块 + 左右各 4)", got)
	}
	// 最上面那两行模块整个落在 quiet zone 里,必须全空。
	for i := 0; i < 2; i++ {
		if strings.TrimSpace(lines[i]) != "" {
			t.Errorf("第 %d 行落在 quiet zone 里却有墨:%q", i, lines[i])
		}
	}
}

// 反色时 quiet zone 必须**跟着底色走**。
// 只反码不反四周,会在码外留一圈墨 —— 那等于把 quiet zone 抹掉了。
func TestQRInvertsTheQuietZoneToo(t *testing.T) {
	out := qrHalfBlocks(3, diagonal, true)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if strings.TrimSpace(lines[0]) == "" {
		t.Error("反色时最外圈是空的 —— quiet zone 没跟着反,码外会是一圈墨的反面")
	}
	if !strings.Contains(lines[0], "█") {
		t.Errorf("反色后的 quiet zone 应当是满的:%q", lines[0])
	}
}

// 深色模块真的落在它该在的位置上。
// 一张画错位置的码在终端里看起来完全正常。
func TestQRPutsInkWhereTheModuleIsDark(t *testing.T) {
	out := qrHalfBlocks(2, func(x, y int) bool { return x == 0 && y == 0 }, false)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	// quiet=4 ⇒ 模块 (0,0) 在第 4 列;y=0 与 y=1 合成同一行,即 y 从 -4 起第 2 行。
	row := lines[2]
	runes := []rune(row)
	if runes[4] != '▀' {
		t.Errorf("(0,0) 深、(0,1) 浅,应当是上半块 ▀,得到 %q(整行 %q)", runes[4], row)
	}
	if runes[5] != ' ' {
		t.Errorf("(1,0) 与 (1,1) 都浅,应当是空格,得到 %q", runes[5])
	}
}

// 反色必须**逐格**反,不是整体换个字符。
func TestQRInvertFlipsEveryModule(t *testing.T) {
	normal := qrHalfBlocks(3, diagonal, false)
	inverted := qrHalfBlocks(3, diagonal, true)
	if normal == inverted {
		t.Fatal("反色没有改变任何东西")
	}
	// 对角线上那些原本有墨的格子,反色后必须没墨。
	nl := strings.Split(strings.TrimRight(normal, "\n"), "\n")
	il := strings.Split(strings.TrimRight(inverted, "\n"), "\n")
	for i := range nl {
		for j, r := range []rune(nl[i]) {
			ir := []rune(il[i])[j]
			if r == '█' && ir == '█' {
				t.Fatalf("第 %d 行第 %d 格反色前后都是满的", i, j)
			}
		}
	}
}

// 没有码可画时**返回空串,不是一张空白的图**。
// 一张空白的二维码看起来像「画出来了、只是扫不出来」,会让人去查扫描器。
func TestQRRendersNothingWithoutACode(t *testing.T) {
	if got := qrHalfBlocks(0, diagonal, false); got != "" {
		t.Errorf("size=0 画出了东西:%q", got)
	}
	if got := qrHalfBlocks(5, nil, false); got != "" {
		t.Errorf("没有取模块的函数却画出了东西:%q", got)
	}
}
