package leakcheck

// countryLanguages 把国家码映射到「在那里上网不显眼的**主要语言**」。
//
// **它回答的不是「官方语言是什么」**,而是「一个从这个出口出来的浏览器,报什么
// 语言是不扎眼的」。所以每一项都刻意宽松:多语言国家全列上,漏一个的代价是
// 冤枉一个正常用户,而那比漏报糟得多(与 countryTimeAreas 同一条偏置)。
//
// **表里没有的国家一律「判不出来」,不猜。** 与「国旗只在能证明时给」同一条纪律:
// 编一个答案比不报更糟。
var countryLanguages = map[string][]string{
	"US": {"en", "es"},
	"CA": {"en", "fr"},
	"GB": {"en"},
	"IE": {"en"},
	"AU": {"en"},
	"NZ": {"en"},
	"SG": {"en", "zh", "ms", "ta"},
	"HK": {"zh", "en"},
	"TW": {"zh", "en"},
	"CN": {"zh", "en"},
	"JP": {"ja", "en"},
	"KR": {"ko", "en"},
	"DE": {"de", "en"},
	"AT": {"de", "en"},
	"CH": {"de", "fr", "it", "en"},
	"FR": {"fr", "en"},
	"NL": {"nl", "en"},
	"BE": {"nl", "fr", "de", "en"},
	"ES": {"es", "ca", "en"},
	"IT": {"it", "en"},
	"PT": {"pt", "en"},
	"SE": {"sv", "en"},
	"NO": {"no", "nb", "nn", "en"},
	"DK": {"da", "en"},
	"FI": {"fi", "sv", "en"},
	"PL": {"pl", "en"},
	"CZ": {"cs", "en"},
	"RU": {"ru", "en"},
	"UA": {"uk", "ru", "en"},
	"TR": {"tr", "en"},
	"BR": {"pt", "en"},
	"MX": {"es", "en"},
	"AR": {"es", "en"},
	"IN": {"en", "hi"},
	"ID": {"id", "en"},
	"MY": {"ms", "en", "zh"},
	"TH": {"th", "en"},
	"VN": {"vi", "en"},
	"PH": {"en", "tl"},
	"IL": {"he", "en"},
	"AE": {"ar", "en"},
	"ZA": {"en", "af"},
}
