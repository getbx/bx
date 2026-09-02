package cli

import "strings"

// qrHalfBlocks 把一张二维码画成终端里的半块字符。
//
// **一个模块一个字符宽、半个字符高** —— 终端单元大约是 1:2,所以半块渲染出来
// 的模块接近正方形。整块渲染(每模块两个字符宽)也方正,但高度翻倍:一条 vless
// reality 链接约 250 字节,二维码约 57×57 模块,整块要 57 行,半块只要 29 行。
//
// **quiet zone 不是装饰,是规范的一部分**:二维码四周必须留 4 个模块的空白,
// 少了它扫描器可能定位不到。终端里尤其要紧 —— 上一行的提示文字会紧贴着码。
//
// dark 报告一个模块是不是**深色(有墨)**。invert 为真时整张反色。
//
// **极性要说清楚**:默认按「深色模块=有墨」画,在**浅色终端**上是正确极性
// (深码浅底,规范要求的那一种)。深色终端上要加 invert —— 现代扫描器多数能
// 认反色,但不是全部,而一张扫不出来的码与没有这个功能完全一样。
func qrHalfBlocks(size int, dark func(x, y int) bool, invert bool) string {
	if size <= 0 || dark == nil {
		return ""
	}
	const quiet = 4
	ink := func(x, y int) bool {
		if x < 0 || y < 0 || x >= size || y >= size {
			return invert // quiet zone 跟着底色走,否则反色时四周留一圈墨
		}
		if invert {
			return !dark(x, y)
		}
		return dark(x, y)
	}

	var b strings.Builder
	// 每行吃两行模块:上半格与下半格。
	for y := -quiet; y < size+quiet; y += 2 {
		for x := -quiet; x < size+quiet; x++ {
			top, bottom := ink(x, y), ink(x, y+1)
			switch {
			case top && bottom:
				b.WriteRune('█')
			case top:
				b.WriteRune('▀')
			case bottom:
				b.WriteRune('▄')
			default:
				b.WriteRune(' ')
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}
