package cli

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/getbx/bx/internal/stats"
)

// 进度**按字节报,不按秒报**。
//
// 2026-09-18 真机:一个 39MB 的包经隧道下了十几分钟,而菜单上唯一的数字是
// `Downloading and installing… 199s`。一个时钟在下载已经死掉之后照样在涨 ——
// 它结构上答不了用户唯一想问的那个问题(「是不是卡住了」),于是那个问题只能
// 由人来问。一个朝着已知终点走的字节数自己就证明自己活着。
func TestFormatDownloadProgressReportsBytesAgainstTheKnownTotal(t *testing.T) {
	got := formatDownloadProgress(12<<20, 40794326)
	for _, want := range []string{"12.0 MB", "38.9 MB", "31%"} {
		if !strings.Contains(got, want) {
			t.Fatalf("进度行 %q 里没有 %q", got, want)
		}
	}
}

// 服务端没给 Content-Length 时**绝不编一个百分比**。
// 「不知道总共多大」与「知道、正好是这么多」是两件事,压成一个数只会让那个
// 百分比在下载过半时突然跳一下,而用户无从分辨那是进度还是错的。
func TestFormatDownloadProgressNeverInventsAPercentageWithoutATotal(t *testing.T) {
	got := formatDownloadProgress(12<<20, -1)
	if !strings.Contains(got, "12.0 MB") {
		t.Fatalf("进度行 %q 没报已下多少", got)
	}
	if strings.Contains(got, "%") {
		t.Fatalf("没有总量却报了百分比: %q", got)
	}
}

// 打不打下一行,两条判据各管一头。
func TestShouldEmitProgressCoversBothTheFastAndTheCrawlingDownload(t *testing.T) {
	step := int64(progressStepBytes)
	cases := []struct {
		name  string
		done  int64
		last  int64
		since time.Duration
		want  bool
	}{
		// 下得快:按字节给细粒度,而总行数随包大小有界(1 MiB 一行)。
		{"够一步了", step, 0, 0, true},
		{"差一点", step - 1, 0, time.Second, false},
		// 下得极慢:心跳仍要证明自己活着 —— 而那正是用户会怀疑卡住的时候。
		// 少了这一条,一个 5 KB/s 的下载可以三分多钟一个字不吭。
		{"几乎没进展但到心跳了", 1024, 0, progressHeartbeat, true},
		{"两条都不满足", 1024, 0, time.Second, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldEmitProgress(tc.done, tc.last, tc.since); got != tc.want {
				t.Fatalf("shouldEmitProgress(%d,%d,%v) = %v, want %v", tc.done, tc.last, tc.since, got, tc.want)
			}
		})
	}
}

// 进度 reader **不许改变读到的字节** —— 它只是旁听。
func TestProgressReaderPassesEveryByteThroughUnchanged(t *testing.T) {
	payload := strings.Repeat("bx", 3<<19) // 3 MiB
	var lines []string
	r := newProgressReader(strings.NewReader(payload), int64(len(payload)),
		func(line string) { lines = append(lines, line) })
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != payload {
		t.Fatalf("读出来的字节变了:%d vs %d", len(got), len(payload))
	}
	if len(lines) == 0 {
		t.Fatal("一行进度都没报")
	}
	last := lines[len(lines)-1]
	if !strings.Contains(last, "100%") {
		t.Fatalf("最后一行不是 100%%:%q(收尾那一行必须打,否则进度停在 97%% 然后画面一跳)", last)
	}
}

// 接线:真的走一遍 HTTP,进度必须来自**真正到达的字节**,而内容一个字节不许变。
//
// 上面那几条测的是判据;这一条测的是「判据被接在了下载路径上」——本仓库反复
// 栽在这一半上(判据写对了、测试盖着,而把真实输入递给它那根线没人守)。
func TestDownloadBytesReportingCountsTheBytesThatActuallyArrive(t *testing.T) {
	payload := []byte(strings.Repeat("bx", 2<<19)) // 2 MiB
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	var lines []string
	got, err := downloadBytesReporting(context.Background(), server.Client(), server.URL,
		func(line string) { lines = append(lines, line) })
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("下载内容变了:%d vs %d 字节", len(got), len(payload))
	}
	if len(lines) == 0 {
		t.Fatal("一行进度都没有 —— 判据接上了,但这条路上没人喂它")
	}
	last := lines[len(lines)-1]
	if !strings.Contains(last, "100%") || !strings.Contains(last, stats.HumanBytes(int64(len(payload)))) {
		t.Fatalf("收尾那一行没报满:%q", last)
	}
}

// 不带 reporter 的那条路(清单、签名)必须**一个字都不打** —— 几百字节的元数据
// 报进度只是噪声,而噪声会把真正要看的那几行淹掉。
func TestDownloadBytesContextStaysSilent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("tiny"))
	}))
	defer server.Close()
	// downloadBytesContext 走的是 emit==nil 那一支;它若开始报进度,这里会 panic。
	if _, err := downloadBytesContext(context.Background(), server.Client(), server.URL); err != nil {
		t.Fatal(err)
	}
}

// 接线的最后一跳:`downloadBytes`(三个大包的下载入口)必须真的把 reporter 传下去。
//
// 少了这一条,把那个实参改成 nil 全仓一行不红,而用户看到的是进度永远不出现、
// 菜单永远退回那句只有秒数的话 —— 也就是这次改动等于没做。
func TestDownloadBytesActuallyReportsProgress(t *testing.T) {
	payload := []byte(strings.Repeat("bx", 2<<19)) // 2 MiB
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	var sink bytes.Buffer
	restore := downloadProgressOut
	downloadProgressOut = &sink
	defer func() { downloadProgressOut = restore }()

	if _, err := downloadBytes(server.Client(), server.URL); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sink.String(), "100%") {
		t.Fatalf("downloadBytes 没有报出进度 —— reporter 那一跳断了。实际输出:%q", sink.String())
	}
}
