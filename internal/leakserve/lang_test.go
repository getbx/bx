package leakserve

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/getbx/bx/internal/leakcheck"
)

// **页面按语言拿结论,CLI 那半永远英文。** 菜单切成中文之后「检查泄漏」蹦出一整页英文
// 是同一个产品里最突兀的一处(所有者 2026-09-28)。`/report?lang=zh-Hans` 给页面的是
// 中文;送进 reports 通道(CLI 打印、agent 解析)的仍是英文那份。骨架标题同理经页面
// 数据下发,页面自己不抄一份译文。
func TestReportIsLocalizedForThePageButStaysEnglishOnTheChannel(t *testing.T) {
	srv := newTestServer(t)
	body := strings.NewReader(`{}`)
	req, err := http.NewRequest(http.MethodPost, "http://"+srv.Addr().String()+"/report?t="+srv.Token()+"&lang=zh-CN", body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", srv.Origin())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	// Section 只有 MarshalJSON,解回 Report 会失败;页面看的就是这三个字段。
	var got struct {
		Findings []struct{ ID, Title, Summary string } `json:"findings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var carrier struct{ ID, Title, Summary string }
	for _, f := range got.Findings {
		if f.ID == leakcheck.FindingCarrier {
			carrier = f
		}
	}
	if !strings.HasPrefix(carrier.Summary, "未检查") || carrier.Title != "谁在承载你的流量" {
		t.Fatalf("page must get Chinese for lang=zh-CN, got %q / %q", carrier.Title, carrier.Summary)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	en := srv.Wait(ctx)
	c := findByID(t, en, leakcheck.FindingCarrier)
	if !strings.HasPrefix(c.Summary, "Not checked") || c.Title != "Who carries your traffic" {
		t.Fatalf("the channel copy must stay English, got %q / %q", c.Title, c.Summary)
	}
}

// 页面骨架的标题译文由服务端下发(`TitlesJSON`),中文页面从第一眼就是中文,而不是
// 等结论回来才换。没传 lang 时页面数据里 Lang 为空 —— 由页面按浏览器语言决定。
func TestPageCarriesTheLanguageAndTheTitleTranslations(t *testing.T) {
	srv := newTestServer(t)
	resp := get(t, srv, "/?t="+srv.Token()+"&lang=zh-Hans")
	defer resp.Body.Close()
	page := readAll(t, resp)
	if !strings.Contains(page, `var LANG = "zh-Hans"`) {
		t.Fatalf("page must carry the requested language, got no LANG line")
	}
	if !strings.Contains(page, "谁在承载你的流量") {
		t.Fatal("page must carry the Chinese skeleton titles")
	}
	resp2 := get(t, srv, "/?t="+srv.Token())
	defer resp2.Body.Close()
	if page2 := readAll(t, resp2); !strings.Contains(page2, `var LANG = ""`) {
		t.Fatal("without ?lang the page must leave the choice to the browser (empty LANG)")
	}
}

func findByID(t *testing.T, r leakcheck.Report, id string) leakcheck.Finding {
	t.Helper()
	for _, f := range r.Findings {
		if f.ID == id {
			return f
		}
	}
	t.Fatalf("no finding %s", id)
	return leakcheck.Finding{}
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
