package corestartfailure

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 记录里**只有码**,不许有自由文本。
//
// 这条是这份记录整个设计的承重点:它跨进程从 Core 递给 Guardian,而 Guardian
// 的失败码会一路走到用户面前。一个不含自由文本的记录**按构造**漏不出路径、
// 链接与凭据 —— 少了这条约束,下一个人往里加一个 `detail` 字段就把
// `vless://<uuid>@…` 送出了 root-only 的 Core 日志。
//
// 判据打在**序列化出来的字节**上,不是打在结构体字段名上:一个
// `Detail string` 字段只要带上 json tag 就会出现在字节里,而按字段名列白名单
// 的写法看不见它换了个名字。
func TestRecordCarriesNothingButACode(t *testing.T) {
	b, err := json.Marshal(Record{SchemaVersion: SchemaVersion, PID: 26158, At: time.Now(), Code: "tunnel_unreachable"})
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]any
	if err := json.Unmarshal(b, &generic); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"schema_version": true, "pid": true, "at": true, "code": true}
	for key := range generic {
		if !want[key] {
			t.Fatalf("记录里多了一个字段 %q —— 只有码的记录才漏不出路径/链接/凭据;\n"+
				"要加字段必须先回答「它会不会带上用户的服务器链接」", key)
		}
	}
	for key := range want {
		if _, ok := generic[key]; !ok {
			t.Fatalf("记录里少了字段 %q", key)
		}
	}
}

func TestWriteThenReadRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core-start-failure.json")
	at := time.Now().UTC().Round(0)
	if err := Write(path, Record{SchemaVersion: SchemaVersion, PID: 4242, At: at, Code: "tunnel_unreachable"}); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.PID != 4242 || got.Code != "tunnel_unreachable" || !got.At.Equal(at) {
		t.Fatalf("读回来的记录对不上:%+v", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("记录权限 %v,want 0600 —— 它落在 /var/lib/bx 里,不该被别人读", perm)
	}
}

// 亚秒精度必须活过一次往返。
//
// Guardian 按「at 落在本次健康窗口内」判断这份记录是不是这一次的,而
// Guardian 一段时间里可能连着 spawn 好几个 Core(事故日志里是五个)。
// RFC3339 秒级截断会让两次相邻 spawn 的时间戳落在同一秒上,窗口判据就分不开
// 它们了。
func TestTimestampKeepsSubSecondPrecision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.json")
	at := time.Date(2026, 9, 12, 18, 1, 51, 123456789, time.UTC)
	if err := Write(path, Record{SchemaVersion: SchemaVersion, PID: 1, At: at, Code: "other"}); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if !got.At.Equal(at) {
		t.Fatalf("时间戳被截断了:写 %s 读回 %s", at.Format(time.RFC3339Nano), got.At.Format(time.RFC3339Nano))
	}
}

// 认不出的 schema 是「这一次没说」,不是一份可以将就着用的记录。
func TestReadRefusesAnUnknownSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":99,"pid":1,"at":"2026-09-12T18:01:51Z","code":"tunnel_unreachable"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); !errors.Is(err, ErrUnknownSchema) {
		t.Fatalf("读到 schema_version=99 返回 %v,want ErrUnknownSchema", err)
	}
}

func TestReadRefusesGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.json")
	if err := os.WriteFile(path, []byte("这不是 json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(path); err == nil {
		t.Fatal("坏掉的记录必须报错 —— 静默返回零值会让 pid=0 去和真实 PID 比,而那只是碰巧不相等")
	}
}

func TestReadOfAnAbsentFileIsNotExist(t *testing.T) {
	if _, err := Read(filepath.Join(t.TempDir(), "没有这个文件")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("读不存在的记录返回 %v,want os.ErrNotExist", err)
	}
}

// 删一个本来就不在的文件不是失败。
//
// 它挂在两条路上:spawn 之前的那次预删(第一层防陈旧)与读完之后的那次清理。
// 两条都是**诊断路径**,而这个仓库的规矩是停止/诊断路径不许因为别的事没做成
// 而失败 —— 把「本来就没有」报成错误会让调用方多写一条它不该关心的分支。
func TestRemoveIsHappyWhenThereIsNothingToRemove(t *testing.T) {
	if err := Remove(filepath.Join(t.TempDir(), "没有这个文件")); err != nil {
		t.Fatalf("删一个不存在的记录报了错:%v", err)
	}
}

// 写盘是原子的:临时文件 + rename,永远不会让读的人看见半份记录。
func TestWriteLeavesNoTemporaryFileBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "core-start-failure.json")
	if err := Write(path, Record{SchemaVersion: SchemaVersion, PID: 7, At: time.Now(), Code: "other"}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != filepath.Base(path) {
			t.Fatalf("目录里留下了 %q —— 原子写的临时文件没清干净", e.Name())
		}
	}
	if len(entries) != 1 {
		t.Fatalf("目录里有 %d 个文件,want 1", len(entries))
	}
}

func TestWriteFailsLoudlyWhenTheDirectoryIsMissing(t *testing.T) {
	err := Write(filepath.Join(t.TempDir(), "没有这个目录", "r.json"), Record{SchemaVersion: SchemaVersion, PID: 1, At: time.Now(), Code: "other"})
	if err == nil {
		t.Fatal("写不进去必须报错 —— 调用方要据此决定记不记一行日志")
	}
	if strings.TrimSpace(err.Error()) == "" {
		t.Fatal("错误消息是空的")
	}
}

// 被 SIGKILL 在原子写中途,留下的临时文件必须有人清 —— 而那一刀按构造就落在
// 这段窗口附近。
//
// Write 是「CreateTemp → 写 → fsync → rename」,那个 `defer os.Remove` 只对
// 正常返回与 panic 有效;SIGKILL 一个 defer 都不跑。而这份记录的整个使用场景
// 就是「Core 起不来,Guardian 随后强杀它」。Remove 只认最终那个名字,一个
// 碎片都清不掉,于是 /var/lib/bx 变成一个只增不减的目录。
func TestDiscardSweepsTemporariesLeftBehindByAKilledWriter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "core-start-failure.json")
	// 模拟「杀在 CreateTemp 与 Rename 之间」:临时文件在,最终名字不在。
	orphan, err := os.CreateTemp(dir, tempPrefix)
	if err != nil {
		t.Fatal(err)
	}
	orphanName := orphan.Name()
	if err := orphan.Close(); err != nil {
		t.Fatal(err)
	}
	// 另一份是上一轮真的写成了的记录。
	if err := Write(path, Record{SchemaVersion: SchemaVersion, PID: 1, At: time.Now(), Code: "other"}); err != nil {
		t.Fatal(err)
	}

	if err := Discard(path); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if _, err := os.Stat(orphanName); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("被强杀的那次写留下的临时文件还在(stat=%v)—— /var/lib/bx 会变成\n"+
			"一个只增不减的目录,而下一个人在事故现场看到的是一堆来路不明的碎片", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("记录本身没被删掉(stat=%v)—— Discard 首先得是一次 Remove", err)
	}
}

// Discard 只扫自己那个前缀,**同目录里别人的文件一个都不许碰**。
//
// 它跑在 /var/lib/bx 上,那里躺着 core-process.json、guardian-state.json、
// maintenance-hold.json、brook / sing-box 二进制、china 列表 —— 一个扫得太宽的
// 清理器会把这台机器的传输二进制删掉,而它挂在**每一次 spawn** 之前。
func TestDiscardTouchesNothingButItsOwnTemporaries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "core-start-failure.json")
	bystanders := []string{"core-process.json", "guardian-state.json", "singbox", ".hidden", "core-start-failure.json.bak"}
	for _, name := range bystanders {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := Discard(path); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	for _, name := range bystanders {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("Discard 删掉了 %q(%v)—— 它跑在 /var/lib/bx 上,那里躺着\n"+
				"传输二进制与 Guardian 自己的状态文件,而它挂在每一次 spawn 之前", name, err)
		}
	}
}

// 目录整个不在也不算失败(诊断路径不许因为别的事没做成而失败)。
func TestDiscardIsHappyWithNothingToSweep(t *testing.T) {
	if err := Discard(filepath.Join(t.TempDir(), "没有这个目录", "r.json")); err != nil {
		t.Fatalf("Discard 在一个不存在的目录上报了错:%v", err)
	}
}
