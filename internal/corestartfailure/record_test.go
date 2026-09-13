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
