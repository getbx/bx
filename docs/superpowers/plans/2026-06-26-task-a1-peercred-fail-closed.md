# A1 — peer-cred 鉴权 fail-closed Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把控制面 POST(改动类)路由的鉴权 `authorizeMutation` 改成 fail-closed —— 凭证验证不了就拒绝,无论平台(修 9b-2a R1 / CWE-636)。

**Architecture:** `authorizeMutation(uid, gotUID)` = `gotUID && uid == 0`(删旧的"`!known→true`"宽松分支);加按平台常量 `peerCredSupported` 仅供 403 错误信息;`control.go` 的 `requireRoot` 调用点改两参 + 据 `peerCredSupported` 选信息。全 Mac 原生可测。

**Tech Stack:** Go 1.26.3;改 `internal/supervisor` 现有 `peercred.go`/`peercred_linux.go`/`peercred_other.go`/`control.go`(9b-2a 产物)。

## Global Constraints

- 模块 `github.com/getbx/bx`,Go 1.26.3。TDD,纯逻辑免 root,Mac 原生跑。
- 提交信息中文 conventional + 结尾 `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`。在 master 直接提交。
- fail-closed everywhere(含 darwin):`authorizeMutation(uid, gotUID) = gotUID && uid == 0`。删宽松分支。
- `peerCredSupported`:`peercred_linux.go`=true、`peercred_other.go`=false,**仅供 403 信息,不参与决策**。
- 保留 `control.go` `requireRoot` 现有的 `conn == nil → 放行`(httptest 旁路)。
- 现有签名(9b-2a):`peerCredUID(conn net.Conn) (uint32, bool)`(第二返回值=是否提到 uid);`authorizeMutation(uid uint32, known bool) bool`(本任务改两参语义/逻辑);`requireRoot(w, r)` 在 `control.go` 调 `peerCredUID`+`authorizeMutation`。**实现前打开这四个文件核对真实现状。**

---

### Task 1: authorizeMutation fail-closed + peerCredSupported + 调用点

**Files:**
- Modify: `internal/supervisor/peercred.go`(`authorizeMutation` 逻辑/注释)
- Modify: `internal/supervisor/peercred_linux.go`(加 `const peerCredSupported = true`)
- Modify: `internal/supervisor/peercred_other.go`(加 `const peerCredSupported = false`)
- Modify: `internal/supervisor/control.go`(`requireRoot` 调用点 + 403 信息)
- Modify: `internal/supervisor/peercred_test.go`(`TestAuthorizeMutation` 改两参 + 4 用例)

**Interfaces:**
- Produces:
  - `func authorizeMutation(uid uint32, gotUID bool) bool`(= `gotUID && uid == 0`)
  - `const peerCredSupported bool`(按平台:linux true / other false)
- Consumes: 现有 `peerCredUID(conn net.Conn) (uint32, bool)`(不变)。

- [ ] **Step 1: 改测试(失败)**

打开 `internal/supervisor/peercred_test.go`,把现有 `TestAuthorizeMutation`(三参 `{uid, known, want}`)替换为两参版:
```go
func TestAuthorizeMutation(t *testing.T) {
	cases := []struct {
		name   string
		uid    uint32
		gotUID bool
		want   bool
	}{
		{"linux-root", 0, true, true},        // 提取成功且 root → 放行
		{"linux-nonroot", 1000, true, false}, // 提取成功但非 root → 拒
		{"linux-extract-failed", 0, false, false}, // 提取失败(uid 不可信)→ 拒(fail-closed,本次核心)
		{"no-peercred", 1000, false, false},  // darwin/拿不到 → 拒(uid 被忽略)
	}
	for _, c := range cases {
		if got := authorizeMutation(c.uid, c.gotUID); got != c.want {
			t.Errorf("%s: authorizeMutation(%d,%v)=%v want %v", c.name, c.uid, c.gotUID, got, c.want)
		}
	}
}
```

- [ ] **Step 2: 跑红**

Run: `go test ./internal/supervisor/ -run Authorize -v`
Expected: 编译失败 / FAIL(旧 `authorizeMutation` 仍是三参或宽松逻辑,与两参调用/期望不符)。

- [ ] **Step 3: 改实现**

`internal/supervisor/peercred.go` —— 替换 `authorizeMutation`:
```go
// authorizeMutation:POST(改动类)路由的鉴权决策(纯函数)。
// fail-closed:凭证验证不了就拒绝(CWE-636 fail-secure)。
//   - 提取成功且 uid==0(root) → 放行
//   - 提取失败(gotUID=false:Linux 异常 conn / darwin 拿不到 peer-cred)→ 拒
func authorizeMutation(uid uint32, gotUID bool) bool {
	return gotUID && uid == 0
}
```

`internal/supervisor/peercred_linux.go` —— 加(文件内,`//go:build linux` 下):
```go
// peerCredSupported 报告本平台能否经 OS 取 peer-cred(决定 403 信息措辞,不参与决策)。
const peerCredSupported = true
```

`internal/supervisor/peercred_other.go` —— 加(`//go:build !linux` 下):
```go
// peerCredSupported=false:本平台暂不取 peer-cred(darwin 待实现 LOCAL_PEERCRED)。
const peerCredSupported = false
```

`internal/supervisor/control.go` —— `requireRoot` 调用点改两参 + 据 `peerCredSupported` 选 403 信息。**打开现有 `requireRoot` 核对真实代码**,把对 `peerCredUID`+`authorizeMutation` 的调用改为:
```go
	uid, gotUID := peerCredUID(conn)
	if !authorizeMutation(uid, gotUID) {
		msg := "改动类命令需 root"
		if !peerCredSupported {
			msg = "此平台暂不支持 peer-cred,改动类已拒绝;macOS daemon 待实现 LOCAL_PEERCRED"
		}
		writeJSON(w, http.StatusForbidden, controlResponse{Status: "error", Error: msg})
		return false
	}
	return true
```
(保留 `requireRoot` 前面的 `conn == nil → return true` 旁路与 method gate 原样。)

- [ ] **Step 4: 跑绿 + 全量**

Run:
```bash
go test ./internal/supervisor/ -run Authorize -v
go test ./internal/supervisor/ ./internal/mcp/ ./internal/cli/ && go vet ./internal/supervisor/ && go build ./...
GOOS=linux go vet ./internal/supervisor/ && GOOS=darwin GOARCH=arm64 go build -o /dev/null ./...
GOOS=linux go vet -tags integration ./internal/supervisor/
```
Expected: 4 个 Authorize 用例 PASS;全套件绿;两平台编译过(`peerCredSupported` 两个常量各自满足);integration vet 干净。
注:若有其它地方调用旧三参 `authorizeMutation`(全局 grep `authorizeMutation`),一并改两参;9b-2a 的 control.go `requireRoot` 是唯一生产调用点。

- [ ] **Step 5: 提交**

```bash
git add internal/supervisor/peercred.go internal/supervisor/peercred_linux.go internal/supervisor/peercred_other.go internal/supervisor/control.go internal/supervisor/peercred_test.go
git commit -m "fix(supervisor): peer-cred 鉴权 fail-closed(修 R1/CWE-636,改动类凭证验不了即拒)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review(对照 spec)

**Spec 覆盖:**
- fail-closed `authorizeMutation(uid, gotUID)=gotUID&&uid==0` → Task 1 Step 3。
- `peerCredSupported` 平台常量仅供 403 信息 → Task 1 Step 3(两平台文件 + control.go 用法)。
- requireRoot 调用点两参 + 信息选择 + 保留 conn==nil 旁路 → Task 1 Step 3。
- 测试 4 用例(重点 Linux 提取失败 fail-closed)→ Task 1 Step 1。
- 全 Mac 可测 + 两平台编译 → Task 1 Step 4。

**占位扫描:** 无 TBD;每步完整代码。control.go 的"打开核对真实 requireRoot"是因为改的是 9b-2a 现有代码、不臆造其确切行号/上下文,非占位。

**类型一致性:** `authorizeMutation(uid uint32, gotUID bool) bool`(Step 3)与测试调用(Step 1)、control.go 调用(Step 3)一致;`peerCredSupported` 两平台常量与 control.go 用法一致;复用 `peerCredUID(conn)(uint32,bool)`/`writeJSON`/`controlResponse`(9b-2a,不变)。
