package guardian

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"testing"
	"time"
)

// 字段存在不等于它活过一次 HTTP 往返 —— 上一期就有一条守卫栽在这上面。
// 这里走真的 handler、真的 JSON 编解码。
func TestArmedHoldSurvivesTheStatusHTTPHop(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.ArmMaintenanceHold(HoldReasonUpgrade, time.Now()); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	NewLocalAPI(env.manager).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/status", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var got Status
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.MaintenanceHold == nil || got.MaintenanceHold.Reason != HoldReasonUpgrade {
		t.Fatalf("挂起没活过 HTTP 往返: %s", recorder.Body.String())
	}
	if got.MaintenanceHold.ExpiresAt.IsZero() {
		t.Fatal("到期时刻丢了 —— 消费方无从判断它还算不算数")
	}
	if !slices.Contains(got.Capabilities, CapabilityMaintenanceHold) {
		t.Fatalf("能力没声明: %v", got.Capabilities)
	}
}

// **desired 与挂起必须来自同一次读盘**(设计取舍⑥)。
//
// 发布出去的这对值是下游全部判断的输入:bx status 的挂起那一行直接印
// 「desired 仍是 %s」,attachObservation 把同一个值喂给 observe.Intent,而
// Diverge 的两条分支都按它分叉。挂起从磁盘读、desired 从内存读,一次应答里就
// 能出现两者互不相干的组合 —— 而内存那一份**已经会撒谎**:needsAttention 把
// 调用方传进来的常量写进 status.Desired,好几处传的是字面量 DesiredOn 而磁盘
// 写着 off。调谐器与 Diverge 于是能在同一台机器的同一瞬间得出相反结论。
func TestStatusPublishesDesiredAndHoldFromTheSameRead(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOff); err != nil {
		t.Fatal(err)
	}
	if err := env.store.ArmMaintenanceHold(HoldReasonUpgrade, time.Now()); err != nil {
		t.Fatal(err)
	}
	// 内存里那一份撒谎的样子,原样复刻:needsAttention 的几个调用点传的就是
	// 这个字面量,而磁盘写着 off。
	env.manager.needsAttention(DesiredOn, "core_unexpected_exit")
	if got := env.manager.Status().Desired; got != DesiredOn {
		t.Fatalf("测试前提不成立:内存里应当是那句谎话,实际 %q", got)
	}

	recorder := httptest.NewRecorder()
	NewLocalAPI(env.manager).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/status", nil))
	var got Status
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.MaintenanceHold == nil {
		t.Fatalf("测试前提不成立:这一格该发布一张挂起: %s", recorder.Body.String())
	}
	if got.Desired != DesiredOff {
		t.Fatalf("发布的 desired = %q,而挂起是从磁盘读的、磁盘写着 off:两者必须同源", got.Desired)
	}
}

// 而**读不出来**时不许拿磁盘的沉默去覆盖信念:发布内存里那一份,挂起键缺席。
// 「问不出来」不是「用户不想要保护」——那正是本仓库 Tristate 那条纪律。
func TestUnreadableIntentKeepsTheBelievedDesired(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.SaveDesired(DesiredOn); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env.store.Store.paths.MaintenanceHold, []byte(`{"schema_version":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	env.manager.needsAttention(DesiredOn, "core_unexpected_exit")

	recorder := httptest.NewRecorder()
	NewLocalAPI(env.manager).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/status", nil))
	var got Status
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Desired != DesiredOn {
		t.Fatalf("一次读盘失败不该把「用户要保护」抹成 %q", got.Desired)
	}
	if got.MaintenanceHold != nil {
		t.Fatalf("读不出来时不得编一个挂起出来: %+v", got.MaintenanceHold)
	}
}

// 过期的挂起不发布 —— 键缺席的意思是「没有挂起」,而不是「有一个不算数的」。
//
// **判据必须是那个键本身,不能是「响应体里出现过 maintenance_hold 这几个字」**:
// 能力名逐字就是 maintenance_hold,它恒在 capabilities 数组里,于是一条子串断言
// 无论实现对错都恒红 —— 这是本计划反复点名的那类「测的是相邻的东西」。
func TestExpiredHoldIsNotPublished(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.ArmMaintenanceHold(HoldReasonUpgrade, time.Now().Add(-2*MaintenanceHoldDuration)); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	NewLocalAPI(env.manager).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/status", nil))
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if _, present := raw["maintenance_hold"]; present {
		t.Fatalf("发布了一个已过期的挂起: %s", recorder.Body.String())
	}
}

// 变更类响应(POST /v1/up、/v1/down)也要带上——菜单的开关读的正是那份响应。
func TestMutationResponsesCarryMaintenanceHoldField(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.ArmMaintenanceHold(HoldReasonUpgrade, time.Now()); err != nil {
		t.Fatal(err)
	}
	status := statusWithVersions(env.manager, LocalAPIOptions{})
	if status.MaintenanceHold == nil {
		t.Fatal("statusWithVersions 没附挂起:菜单的 Turn Off 只看得到这份响应")
	}
}

// holdOnlyController 是一个**没有**路径恢复能力的 Controller:它把
// observableStatus 逼进 `recoveries == nil` 那条早返回。
//
// 存在的理由是变异 2 那类假绿:attachMaintenanceHold 只挂在两个 return 中的一个
// 上时,拿 fakeController(它实现了 PathRecoveryController)写的测试照样全绿,
// 而真机上一台没接路径恢复的 Guardian 一个字都不会说。
type holdOnlyController struct {
	status Status
	hold   *MaintenanceHoldStatus
}

func (c *holdOnlyController) Status() Status             { return c.status }
func (c *holdOnlyController) Up(context.Context) error   { return nil }
func (c *holdOnlyController) Down(context.Context) error { return nil }

func (c *holdOnlyController) PublishedIntent() (DesiredState, *MaintenanceHoldStatus, bool) {
	return c.status.Desired, c.hold, true
}

func TestStatusCarriesHoldOnTheNoPathRecoveryBranch(t *testing.T) {
	controller := &holdOnlyController{
		status: Status{SchemaVersion: 1, Desired: DesiredOn, Protection: ProtectionOff},
		hold:   &MaintenanceHoldStatus{Reason: HoldReasonUpgrade, ExpiresAt: time.Now().Add(MaintenanceHoldDuration)},
	}
	if _, isPathRecovery := any(controller).(PathRecoveryController); isPathRecovery {
		t.Fatal("这个替身必须**不**实现 PathRecoveryController,否则它测的是另一条分支")
	}
	recorder := httptest.NewRecorder()
	NewLocalAPI(controller).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/status", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var got Status
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.MaintenanceHold == nil || got.MaintenanceHold.Reason != HoldReasonUpgrade {
		t.Fatalf("没有路径恢复的那条早返回上丢了挂起: %s", recorder.Body.String())
	}
}

// 挂起读不出来时不许伪装成「没有挂起」以外的东西:MaintenanceHoldStatus 对
// 一个答不了这个问题的 Store 返回 nil(键缺席),而**能力照样声明** ——
// 能力说的是「这一版认识挂起」,不是「此刻答得出来」。
func TestUnreadableHoldPublishesNoKeyButKeepsCapability(t *testing.T) {
	env := newManagerTestEnv(t)
	if err := env.store.ArmMaintenanceHold(HoldReasonUpgrade, time.Now()); err != nil {
		t.Fatal(err)
	}
	// 把文件写坏:LoadMaintenanceHold 对坏 schema 报错(不是答「没有挂起」)。
	if err := os.WriteFile(env.store.Store.paths.MaintenanceHold, []byte(`{"schema_version":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	NewLocalAPI(env.manager).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/status", nil))
	var got Status
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.MaintenanceHold != nil {
		t.Fatalf("读不出来时不得编一个挂起出来: %+v", got.MaintenanceHold)
	}
	if !slices.Contains(got.Capabilities, CapabilityMaintenanceHold) {
		t.Fatalf("能力是编译期常量,不该随一次读盘失败消失: %v", got.Capabilities)
	}
}
