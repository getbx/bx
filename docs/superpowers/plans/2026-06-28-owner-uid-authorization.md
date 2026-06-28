# 业主 uid 授权(③ onboarding Slice ③-1)Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 控制面改动类授权从 root-only 精化为「root 或配置的业主 uid」(`owner_uid==0` 退回 root-only),并让 `sudo bx setup` 捕获 `$SUDO_UID` 写入 `owner_uid`——让业主(及其以业主身份跑的 agent)免 root 操作 bx。

**Architecture:** `authorizeMutation` 加第三参 `ownerUID`;`controlServer` 持 `ownerUID`,`requireRoot` 改 `*controlServer` 方法;`run.go` 把 `cfg.OwnerUID` 经 `serveControl`/`newControlMux` 插线进控制面。`config.Config` 加 `OwnerUID`;`bx setup` 经纯函数 `ownerUIDFromEnv` 从 `SUDO_UID` 取业主写进 `minimalConfig`。

**Tech Stack:** Go 1.26.3;改 `internal/supervisor`(peercred/control/run)、`internal/config`、`internal/setup`。

## Global Constraints

- 模块 `github.com/getbx/bx`,Go 1.26.3。TDD;提交中文 conventional + 结尾 `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`;master 直接提交。
- **fail-closed 不变**:`authorizeMutation(uid, gotUID, ownerUID) = gotUID && (uid==0 || (ownerUID!=0 && uid==ownerUID))`。`ownerUID==0`(未配置)→ 严格 root-only。取 uid 失败(`gotUID==false`)→ 拒。
- 保留 `requireRoot` 现有 `conn==nil → 放行`(httptest 旁路)与 method gate。
- darwin 仍 fail-closed(peer-cred 未实现);业主授权 Linux 生效。
- `owner_uid` yaml 键:`config.Config` 读 `yaml:"owner_uid"`;`setup.minimalConfig` 写 `yaml:"owner_uid,omitempty"`(0 省略)。

---

### Task 1: authorizeMutation 三参 + config.OwnerUID + 控制面插线

**Files:**
- Modify: `internal/supervisor/peercred.go`(`authorizeMutation` 加 ownerUID 参)
- Modify: `internal/supervisor/peercred_test.go`(三参表 + 业主用例)
- Modify: `internal/supervisor/control.go`(`controlServer.ownerUID` 字段;`requireRoot`→方法;`newControlMux`/`serveControl` 加 ownerUID;4 调用点)
- Modify: `internal/supervisor/control_test.go`(2 处 `newControlMux` 调用加 ownerUID 实参)
- Modify: `internal/supervisor/run.go`(`serveControl` 传 `uint32(cfg.OwnerUID)`)
- Modify: `internal/config/config.go`(`Config` 加 `OwnerUID int`)

**Interfaces:**
- Produces:
  - `func authorizeMutation(uid uint32, gotUID bool, ownerUID uint32) bool`
  - `func newControlMux(eng controlEngine, report func() stats.Report, mut mutator, ownerUID uint32) http.Handler`
  - `func serveControl(c *stats.Counters, t tunnelStatser, server, udpMode string, eng controlEngine, mut mutator, ownerUID uint32) (io.Closer, error)`
  - `Config.OwnerUID int`(yaml `owner_uid`)。
- Consumes: 现有 `peerCredUID`/`requireRoot` 旁路/`controlServer` handlers。

- [ ] **Step 1: 改测试(失败)**

把 `internal/supervisor/peercred_test.go` 的 `TestAuthorizeMutation` 整体替换为三参版:
```go
package supervisor

import "testing"

func TestAuthorizeMutation(t *testing.T) {
	cases := []struct {
		name   string
		uid    uint32
		gotUID bool
		owner  uint32
		want   bool
	}{
		{"root-with-owner", 0, true, 1000, true},   // root 永远放行
		{"root-no-owner", 0, true, 0, true},        // 无业主时 root 仍放行
		{"owner", 1000, true, 1000, true},          // 业主放行(本片核心)
		{"nonroot-no-owner", 1000, true, 0, false}, // 无业主 → 退回 root-only(核心)
		{"other-user", 1001, true, 1000, false},    // 非 root 非业主 → 拒
		{"extract-failed", 0, false, 1000, false},  // 取 uid 失败 → fail-closed
	}
	for _, c := range cases {
		if got := authorizeMutation(c.uid, c.gotUID, c.owner); got != c.want {
			t.Errorf("%s: authorizeMutation(%d,%v,%d)=%v want %v", c.name, c.uid, c.gotUID, c.owner, got, c.want)
		}
	}
}
```

- [ ] **Step 2: 跑红**

Run: `go test ./internal/supervisor/ -run Authorize 2>&1 | head`
Expected: 编译失败(`authorizeMutation` 仍二参,与三参调用不符)。

- [ ] **Step 3: 改 authorizeMutation(peercred.go)**

替换 `authorizeMutation`:
```go
// authorizeMutation:改动类路由鉴权(纯函数,fail-closed)。
// 放行 = 成功取到 uid 且(uid 是 root 或 = 配置的业主 uid)。
// ownerUID==0 表示无业主配置 → 退回 root-only(安全默认)。
func authorizeMutation(uid uint32, gotUID bool, ownerUID uint32) bool {
	return gotUID && (uid == 0 || (ownerUID != 0 && uid == ownerUID))
}
```

- [ ] **Step 4: 改 control.go —— controlServer 字段 + requireRoot 方法 + 签名插线**

(a) `controlServer` 结构体加字段(现 `&controlServer{eng: eng, report: report, mut: mut}` 处的结构体定义):加 `ownerUID uint32`。

(b) `newControlMux` 加参数并赋值:
```go
func newControlMux(eng controlEngine, report func() stats.Report, mut mutator, ownerUID uint32) http.Handler {
	cs := &controlServer{eng: eng, report: report, mut: mut, ownerUID: ownerUID}
```
(其余 body 不变。)

(c) `requireRoot` 自由函数改 `*controlServer` 方法,用 `cs.ownerUID`:
```go
func (cs *controlServer) requireRoot(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, controlResponse{Status: "error", Error: "method not allowed"})
		return false
	}
	conn, _ := r.Context().Value(ctxConnKey{}).(net.Conn)
	if conn == nil {
		return true
	}
	uid, gotUID := peerCredUID(conn)
	if !authorizeMutation(uid, gotUID, cs.ownerUID) {
		msg := "改动类命令需 root 或业主"
		if !peerCredSupported {
			msg = "此平台暂不支持 peer-cred,改动类已拒绝;macOS daemon 待实现 LOCAL_PEERCRED"
		}
		writeJSON(w, http.StatusForbidden, controlResponse{Status: "error", Error: msg})
		return false
	}
	return true
}
```

(d) 4 个 handler 调用点(handleSetTransport/handleRehijack/handleCommit/handleRollback,现 `if !requireRoot(w, r) {`)改为 `if !cs.requireRoot(w, r) {`。

(e) `serveControl` 加 `ownerUID uint32` 参,传给 `newControlMux`:
```go
func serveControl(c *stats.Counters, t tunnelStatser, server, udpMode string, eng controlEngine, mut mutator, ownerUID uint32) (io.Closer, error) {
	...
		Handler:           newControlMux(eng, report, mut, ownerUID),
	...
}
```

- [ ] **Step 5: 改 control_test.go(2 处 newControlMux 调用)**

`internal/supervisor/control_test.go` 的两处 `newControlMux(eng, …, mut)` 末尾加 `, 0`(测试用 httptest TCP,conn==nil 旁路,ownerUID 取 0 不影响既有断言):
- `newControlMux(eng, func() stats.Report { return stats.Report{Server: "test-node"} }, nopMutator{})` → `…, nopMutator{}, 0)`
- `newControlMux(eng, func() stats.Report { return stats.Report{Server: "test-node"} }, mut)` → `…, mut, 0)`

- [ ] **Step 6: 改 config.go(OwnerUID 字段)**

`internal/config/config.go` 的 `Config` 结构体加字段(放 Killswitch 附近):
```go
	OwnerUID int `yaml:"owner_uid"` // 业主 uid(sudo bx setup 捕获);0=无业主,控制面退回 root-only
```
`Parse` 无需改(0 合法默认)。

- [ ] **Step 7: 改 run.go(传 ownerUID)**

`internal/supervisor/run.go` 的 `serveControl(counters, lt, serverHost, cfg.UDP.Mode, mutEng, mut)` 改为:
```go
		return serveControl(counters, lt, serverHost, cfg.UDP.Mode, mutEng, mut, uint32(cfg.OwnerUID))
```

- [ ] **Step 8: 跑绿 + 全量**

Run:
```bash
go test ./internal/supervisor/ -run Authorize -v
go build ./... && go vet ./... && go test ./...
GOOS=darwin GOARCH=arm64 go build -o /dev/null ./...
GOOS=linux go vet -tags integration ./internal/supervisor/
```
Expected: 6 个 Authorize 用例 PASS;既有控制面/config 测随签名更新仍绿;全套件绿;两平台编译过。

- [ ] **Step 9: 提交**

```bash
git add internal/supervisor/peercred.go internal/supervisor/peercred_test.go internal/supervisor/control.go internal/supervisor/control_test.go internal/supervisor/run.go internal/config/config.go
git commit -m "feat(supervisor): 控制面授权 root 或业主 uid(③-1 基座)

authorizeMutation 加第三参 ownerUID(root 或业主放行;owner_uid==0 退回 root-only 安全默认);
controlServer 持 ownerUID,requireRoot 改方法;run.go 经 cfg.OwnerUID 插线。config 加 OwnerUID。
让业主(及其以业主身份跑的 agent)免 root 操作 bx。setup 捕获业主见下一任务。

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: bx setup 捕获业主 uid(SUDO_UID)

**Files:**
- Modify: `internal/setup/setup.go`(`minimalConfig` 加 `OwnerUID`;`ownerUIDFromEnv` 纯函数;写配置时填入)
- Create: `internal/setup/setup_test.go`(若不存在;`TestOwnerUIDFromEnv`)或追加到现有 setup 测试文件

**Interfaces:**
- Consumes: Task 1 的 `config.Config.OwnerUID`(yaml `owner_uid`,setup 写、daemon 读)。
- Produces: `func ownerUIDFromEnv(getenv func(string) string) int`;`minimalConfig.OwnerUID`。

- [ ] **Step 1: 写失败测试**

新建/追加 `internal/setup/setup_test.go`:
```go
package setup

import "testing"

func TestOwnerUIDFromEnv(t *testing.T) {
	mk := func(v string) func(string) string { return func(string) string { return v } }
	cases := []struct {
		in   string
		want int
	}{
		{"1000", 1000},
		{"", 0},
		{"abc", 0},
		{"0", 0},
		{" 1001 ", 1001},
		{"-5", 0},
	}
	for _, c := range cases {
		if got := ownerUIDFromEnv(mk(c.in)); got != c.want {
			t.Errorf("ownerUIDFromEnv(%q)=%d want %d", c.in, got, c.want)
		}
	}
}
```
(若 `internal/setup/setup_test.go` 已存在,把上面的 `TestOwnerUIDFromEnv` 追加进去,不重复 package 行。)

- [ ] **Step 2: 跑红**

Run: `go test ./internal/setup/ -run OwnerUID 2>&1 | head`
Expected: 编译失败(`undefined: ownerUIDFromEnv`)。

- [ ] **Step 3: 改 setup.go**

(a) import 加 `"strconv"` 与 `"strings"`(现有 import 块)。

(b) `minimalConfig` 加字段:
```go
type minimalConfig struct {
	Server     string `yaml:"server"`
	Global     bool   `yaml:"global"`
	Killswitch bool   `yaml:"killswitch"`
	OwnerUID   int    `yaml:"owner_uid,omitempty"` // sudo bx setup 的真实用户;0 省略(root-only)
}
```

(c) 加纯函数:
```go
// ownerUIDFromEnv 从 SUDO_UID 取业主 uid(sudo bx setup 的真实用户)。
// 非数字/空/<=0 → 0(无业主,控制面退回 root-only)。注入 getenv 便于免环境单测。
func ownerUIDFromEnv(getenv func(string) string) int {
	n, err := strconv.Atoi(strings.TrimSpace(getenv("SUDO_UID")))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}
```

(d) 写配置处(现 `yaml.Marshal(minimalConfig{Server: link, Global: true, Killswitch: true})`)填入业主:
```go
	b, err := yaml.Marshal(minimalConfig{Server: link, Global: true, Killswitch: true, OwnerUID: ownerUIDFromEnv(os.Getenv)})
```

- [ ] **Step 4: 跑绿 + 全量**

Run:
```bash
go test ./internal/setup/ -run OwnerUID -v
go build ./... && go vet ./... && go test ./...
GOOS=darwin GOARCH=arm64 go build -o /dev/null ./...
```
Expected: `TestOwnerUIDFromEnv` 6 用例 PASS;全套件绿;两平台编译过。

- [ ] **Step 5: 提交**

```bash
git add internal/setup/setup.go internal/setup/setup_test.go
git commit -m "feat(setup): bx setup 捕获业主 uid(SUDO_UID → owner_uid)

sudo bx setup 经 ownerUIDFromEnv 从 SUDO_UID 取真实用户写进 owner_uid(空/非法→0=root-only)。
配合 ③-1 控制面业主授权:业主免 root 操作 bx(为 ③-2 bx pair 免 sudo 配对铺路)。

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review(对照 spec)

**Spec 覆盖:**
- `authorizeMutation` 三参 root-或-业主、`ownerUID==0` 退回 root-only → Task1 Step3 + 测试 Step1。
- `controlServer.ownerUID` + `requireRoot` 方法 + `newControlMux`/`serveControl` 插线 + 4 调用点 → Task1 Step4。
- `config.Config.OwnerUID`(yaml owner_uid)→ Task1 Step6;run.go 传参 → Step7。
- setup 捕获 `SUDO_UID`(`ownerUIDFromEnv` 纯函数 + minimalConfig 字段 + 写入)→ Task2 Step3。
- 测试:authorizeMutation 表(含业主/无业主/他人/取 uid 失败)+ ownerUIDFromEnv 表 → Task1 Step1 + Task2 Step1。
- 保留 conn==nil 旁路 + method gate → Task1 Step4c。

**占位扫描:** 无 TBD;每步完整代码/命令。行号为锚点核对(按内容定位)。

**类型一致性:** `authorizeMutation(uint32,bool,uint32)bool`(Task1 S3)与测试(S1)、requireRoot 调用(S4c)一致;`newControlMux(…,ownerUID uint32)`/`serveControl(…,ownerUID uint32)`(S4)与 run.go 调用 `uint32(cfg.OwnerUID)`(S7)、control_test(S5)一致;`Config.OwnerUID int yaml:"owner_uid"`(S6)与 setup `minimalConfig.OwnerUID int yaml:"owner_uid,omitempty"`(Task2 S3)同键;`ownerUIDFromEnv(func(string)string)int`(Task2 S3)与测试(S1)一致。
