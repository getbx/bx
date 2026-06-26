package supervisor

// authorizeMutation 是 POST(改动类)路由的鉴权决策(纯函数,便于测试)。
// 拿到 peer uid(known)时仅 root 放行;平台取不到 peer-cred(known=false,如 darwin)
// 时开发态宽松放行 —— 真机/生产应收紧(见 peercred_other.go)。
func authorizeMutation(uid uint32, known bool) bool {
	if !known {
		return true // TODO(真机): darwin 收紧为 LOCAL_PEERCRED
	}
	return uid == 0
}
