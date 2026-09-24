package cli

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getbx/bx/internal/corestartfailure"
	"github.com/getbx/bx/internal/elevate"
	"github.com/getbx/bx/internal/supervisor"
)

// —— Linux 上 Core 起不来时说出为什么(known-gaps A9,2026-09-23)——
//
// darwin 上这件事由 Guardian 读 Core 自报的记录来说;linux 走 systemd、不经
// Guardian,记录从来没人读(此前甚至从来没人写:ExecStart 不带那个 flag)。
// 这里让 `bx status` 在「服务要跑却起不来」时读同一份记录、用同一套措辞。

func recordFor(code string) func() (corestartfailure.Record, error) {
	return func() (corestartfailure.Record, error) {
		return corestartfailure.Record{
			SchemaVersion: corestartfailure.SchemaVersion, PID: 42, At: time.Now(), Code: code,
		}, nil
	}
}

func TestLinuxStatusSaysWhyTheServiceCannotStart(t *testing.T) {
	facts := startFailureServers{CurrentHostPort: "203.0.113.92:443"}
	for _, state := range []string{"activating", "failed"} {
		note := linuxStartFailureNote(state, recordFor(supervisor.StartFailureTunnelUnreachable), facts)
		if !strings.Contains(note, "203.0.113.92:443") {
			t.Errorf("%s:服务起不来而记录在,却没说出原因:%q", state, note)
		}
	}
}

// 用户自己停掉的服务不许被说成「起不来」:上一次失败留下的记录在那时是历史,不是现状。
func TestLinuxStatusStaysQuietWhenTheUserStoppedTheService(t *testing.T) {
	for _, state := range []string{"inactive", "active", "unknown", ""} {
		if note := linuxStartFailureNote(state, recordFor(supervisor.StartFailureTunnelUnreachable), startFailureServers{}); note != "" {
			t.Errorf("%s:服务不在「要跑却起不来」的状态,却报了一段启动失败:%q", state, note)
		}
	}
}

// 读不到记录 / 认不出码 ⇒ 一个字都不说,不猜(与 Guardian 那一侧同一条)。
func TestLinuxStatusDoesNotGuessWithoutARecord(t *testing.T) {
	missing := func() (corestartfailure.Record, error) { return corestartfailure.Record{}, os.ErrNotExist }
	if note := linuxStartFailureNote("failed", missing, startFailureServers{}); note != "" {
		t.Errorf("没有记录却说了原因:%q", note)
	}
	broken := func() (corestartfailure.Record, error) { return corestartfailure.Record{}, errors.New("parse") }
	if note := linuxStartFailureNote("failed", broken, startFailureServers{}); note != "" {
		t.Errorf("记录读不动却说了原因:%q", note)
	}
	if note := linuxStartFailureNote("failed", recordFor("no_such_code"), startFailureServers{}); note != "" {
		t.Errorf("认不出的码却说了原因:%q", note)
	}
}

// 非 root 读不到记录(/var/lib/bx 是 drwx------)时,如实说要 sudo 才看得到原因,
// 不退回「bx is not running / Start it」—— 服务正在反复重启时那是假话。
func TestLinuxStatusWithoutRootSaysWhereTheReasonIs(t *testing.T) {
	denied := func() (corestartfailure.Record, error) { return corestartfailure.Record{}, fs.ErrPermission }
	note := linuxStartFailureNote("activating", denied, startFailureServers{})
	if !strings.Contains(note, elevate.Cmd("bx status")) {
		t.Fatalf("读不到记录时没说要 sudo 才看得到原因:%q", note)
	}
	if note2 := linuxStartFailureNote("inactive", denied, startFailureServers{}); note2 != "" {
		t.Fatalf("用户自己停掉的服务也说了「起不来」:%q", note2)
	}
}

// Linux 上 Core 的日志在 journald 里,不在 /var/log/bx.log —— 指错地方的「完整原因
// 在这里」比不给更糟,读到它的人正处在 bx 起不来的时刻。
func TestTheFullReasonPointsAtTheRightLogPerPlatform(t *testing.T) {
	if cmd := coreLogCommandFor("linux"); !strings.Contains(cmd, "journalctl -u bx.service") {
		t.Errorf("linux 的日志命令不对:%q", cmd)
	}
	if cmd := coreLogCommandFor("darwin"); !strings.Contains(cmd, "/var/log/bx.log") {
		t.Errorf("darwin 的日志命令不对:%q", cmd)
	}
}

// **记录只代表最近一次启动。** systemd 每 3 秒重启一次;一次成功的启动之后旧记录
// 必须没了,否则之后任何一次 Core 不在的时刻都会读到一段过期的失败。
func TestRunClearsAStaleRecordBeforeStarting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core-start-failure.json")
	if err := corestartfailure.Write(path, corestartfailure.Record{
		SchemaVersion: corestartfailure.SchemaVersion, PID: 1, At: time.Now(), Code: "other",
	}); err != nil {
		t.Fatal(err)
	}
	var sawRecordWhileRunning bool
	_ = runWithStartFailureRecord(path, func() error {
		_, err := os.Stat(path)
		sawRecordWhileRunning = err == nil
		return nil
	})
	if sawRecordWhileRunning {
		t.Fatal("Core 启动时没有清掉上一次的失败记录 —— 它之后会被当成这一次的原因读出来")
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("一次成功的运行之后失败记录还在")
	}
}

// 接线:bx status 在 Core 不在时真的去问了(判据对,而接线没接上,是这个仓库列过的
// 第六种失效写法)。
func TestStatusActionAsksWhyTheLinuxServiceCannotStart(t *testing.T) {
	src, err := os.ReadFile("cli.go")
	if err != nil {
		t.Fatal(err)
	}
	body, ok := goFunctionBody(string(src), "func statusAction(")
	if !ok {
		t.Fatal("读不出 statusAction —— 守卫读不懂现在的代码了")
	}
	if !strings.Contains(body, "liveLinuxStartFailureNote(") {
		t.Fatal("statusAction 没有调 liveLinuxStartFailureNote —— linux 上起不来的原因不会出现在 bx status 里")
	}
	if strings.Index(body, "liveLinuxStartFailureNote(") > strings.Index(body, "stats.RenderNotRunning()") {
		t.Fatal("RenderNotRunning 排在前面 —— 服务正在反复重启时会先说「没在跑、去启动它」")
	}
}
