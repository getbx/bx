package supervisor

// authorizeMutation:POST(改动类)路由的鉴权决策(纯函数)。
// fail-closed:凭证验证不了就拒绝(CWE-636 fail-secure)。
//   - 提取成功且 uid==0(root) → 放行
//   - 提取失败(gotUID=false:Linux 异常 conn / darwin 拿不到 peer-cred)→ 拒
func authorizeMutation(uid uint32, gotUID bool) bool {
	return gotUID && uid == 0
}
