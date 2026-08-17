package rulereview

// ownerRulesFixture 是项目所有者那份配置的**代表性形状**:24 条手写直连规则,
// 大多是国内大厂域名、其中一条是公有云开放子域(*.myqcloud.com)、若干条被自己
// 更宽的一条盖住(*.apple.com 系)。
//
// 它不是逐字拷贝(那份配置是 0600 root,而且真机上的东西不该进仓库),
// 但**那三个数量级关系是真的**:大多数条目命中 china 列表、有一条危险直连、
// 有一小撮同表冗余。spec 里那次实测的 24 / 22 / 11 就是这个形状。
func ownerRulesFixture() []string {
	return []string{
		"*.qq.com", "*.qpic.cn", "*.qlogo.cn", "*.gtimg.cn", "*.myqcloud.com",
		"*.taobao.com", "*.tmall.com", "*.alicdn.com", "*.aliyun.com",
		"*.baidu.com", "*.bdstatic.com", "*.bilibili.com", "*.hdslb.com",
		"*.163.com", "*.126.net", "*.zhihu.com", "*.zhimg.com",
		"*.jd.com", "*.360buyimg.com", "*.weibo.com", "*.sinaimg.cn",
		"*.apple.com", "ocsp.apple.com", "*.push.apple.com",
	}
}

// ownerRulesChinaListFixture 是一份**够用的** china 列表切片:覆盖上面大多数条目,
// 但刻意不含 apple.com 系(苹果的域名不在 china 直连列表里)。
//
// 用切片而不是真的内嵌列表,是为了让这条测试的**输入是可读的** ——
// 拿 12165 条真列表跑,断言失败时没人看得出为什么。真列表那一侧由
// internal/cli 的组装测试覆盖(Task 5)。
func ownerRulesChinaListFixture() []string {
	return []string{
		"qq.com", "qpic.cn", "qlogo.cn", "gtimg.cn", "myqcloud.com",
		"taobao.com", "tmall.com", "alicdn.com", "aliyun.com",
		"baidu.com", "bdstatic.com", "bilibili.com", "hdslb.com",
		"163.com", "126.net", "zhihu.com", "zhimg.com",
		"jd.com", "360buyimg.com", "weibo.com", "sinaimg.cn",
	}
}
