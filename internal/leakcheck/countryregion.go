package leakcheck

// countryTimeAreas 把 ISO 3166-1 alpha-2 国家码映射到它**合法的 IANA 时区大区**。
//
// IANA 时区名的第一段就是大区(`Asia/Shanghai` → `Asia`),所以这张表足以回答
// 「时钟与出口在不在同一个大洲」。
//
// **一个国家可以有多个合法大区,漏一个就是无端报红。** 美国横跨 America 与
// Pacific(檀香山);俄罗斯横跨 Europe 与 Asia;西班牙、葡萄牙有 Atlantic 的
// 岛屿属地。这些不是边角情况,是真实用户。
//
// **这张表是刻意不全的。** 认不出的国家由判据答「认不出」,而不是猜一个大区去比 ——
// 猜出来的答案是一条随机的红或绿,两者都比不报更糟。收录范围是 VPN 出口常见地 +
// 人口大国;缺哪个补哪个,补的时候连同一条用例一起加。
var countryTimeAreas = map[string][]string{
	// 美洲
	"US": {"America", "Pacific"},
	"CA": {"America"},
	"MX": {"America"},
	"BR": {"America"},
	"AR": {"America"},
	"CL": {"America", "Pacific"},
	"CO": {"America"},
	"PE": {"America"},
	"PA": {"America"},
	"CR": {"America"},
	// 欧洲
	"GB": {"Europe"},
	"IE": {"Europe"},
	"DE": {"Europe"},
	"FR": {"Europe", "Indian", "America", "Pacific"}, // 海外省
	"NL": {"Europe", "America"},
	"BE": {"Europe"},
	"LU": {"Europe"},
	"CH": {"Europe"},
	"AT": {"Europe"},
	"IT": {"Europe"},
	"ES": {"Europe", "Atlantic", "Africa"}, // 加那利、休达
	"PT": {"Europe", "Atlantic"},           // 亚速尔、马德拉
	"SE": {"Europe"},
	"NO": {"Europe", "Arctic", "Atlantic"},
	"DK": {"Europe", "America", "Atlantic"}, // 格陵兰、法罗
	"FI": {"Europe"},
	"IS": {"Atlantic"},
	"PL": {"Europe"},
	"CZ": {"Europe"},
	"SK": {"Europe"},
	"HU": {"Europe"},
	"RO": {"Europe"},
	"BG": {"Europe"},
	"GR": {"Europe"},
	"RS": {"Europe"},
	"HR": {"Europe"},
	"SI": {"Europe"},
	"UA": {"Europe"},
	"MD": {"Europe"},
	"LV": {"Europe"},
	"LT": {"Europe"},
	"EE": {"Europe"},
	"RU": {"Europe", "Asia"},
	"TR": {"Europe"}, // Europe/Istanbul
	// 亚洲
	"CN": {"Asia"},
	"HK": {"Asia"},
	"MO": {"Asia"},
	"TW": {"Asia"},
	"JP": {"Asia"},
	"KR": {"Asia"},
	"SG": {"Asia"},
	"MY": {"Asia"},
	"TH": {"Asia"},
	"VN": {"Asia"},
	"ID": {"Asia"},
	"PH": {"Asia"},
	"IN": {"Asia"},
	"PK": {"Asia"},
	"BD": {"Asia"},
	"KZ": {"Asia"},
	"AE": {"Asia"},
	"SA": {"Asia"},
	"QA": {"Asia"},
	"IL": {"Asia"},
	// 大洋洲
	"AU": {"Australia", "Indian", "Antarctica"},
	"NZ": {"Pacific"},
	// 非洲
	"ZA": {"Africa"},
	"EG": {"Africa"},
	"NG": {"Africa"},
	"KE": {"Africa"},
	"MA": {"Africa"},
}
