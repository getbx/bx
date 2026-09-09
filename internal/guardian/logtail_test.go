package guardian

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTailLinesReturnsTheLastNInFileOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.log")
	if err := os.WriteFile(path, []byte("a\nb\nc\nd\ne\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := tailLines(path, 3)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "c,d,e" {
		t.Fatalf("尾部 3 行 = %v, want c,d,e", got)
	}
}

func TestTailLinesHandlesShortFilesMissingNewlineAndEmpty(t *testing.T) {
	dir := t.TempDir()
	short := filepath.Join(dir, "short.log")
	_ = os.WriteFile(short, []byte("only\ntwo"), 0o600) // 末尾没有换行
	got, err := tailLines(short, 10)
	if err != nil || strings.Join(got, ",") != "only,two" {
		t.Fatalf("行数不足时要给全部且认得末尾无换行:%v %v", got, err)
	}
	empty := filepath.Join(dir, "empty.log")
	_ = os.WriteFile(empty, nil, 0o600)
	got, err = tailLines(empty, 10)
	if err != nil || len(got) != 0 {
		t.Fatalf("空文件 = %v %v, want 空切片无错", got, err)
	}
	if got, err := tailLines(short, 0); err != nil || len(got) != 0 {
		t.Fatalf("n=0 = %v %v", got, err)
	}
}

func TestTailLinesReadsOnlyTheTailOfABigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.log")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	// 200k 行、约 3MB:倒读实现不该把整个文件读进来才取最后 5 行。
	for i := 0; i < 200_000; i++ {
		_, _ = f.WriteString("0123456789\n")
	}
	_, _ = f.WriteString("last-1\nlast-2\nlast-3\nlast-4\nlast-5\n")
	_ = f.Close()
	got, err := tailLines(path, 5)
	if err != nil || strings.Join(got, ",") != "last-1,last-2,last-3,last-4,last-5" {
		t.Fatalf("大文件尾部 = %v %v", got, err)
	}
}

func TestTailLinesReportsMissingFile(t *testing.T) {
	if _, err := tailLines(filepath.Join(t.TempDir(), "nope.log"), 3); err == nil {
		t.Fatal("文件不存在要报错,不能悄悄给空切片(「没读到」≠「日志是空的」)")
	}
}
