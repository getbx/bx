package supervisor

// authorizeMutation:改动类路由鉴权(纯函数,fail-closed)。
// 放行 = 成功取到 uid 且(uid 是 root 或 = 配置的业主 uid)。
// ownerUID==0 表示无业主配置 → 退回 root-only(安全默认)。
func authorizeMutation(uid uint32, gotUID bool, ownerUID uint32) bool {
	return gotUID && (uid == 0 || (ownerUID != 0 && uid == ownerUID))
}
