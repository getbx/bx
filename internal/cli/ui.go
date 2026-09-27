package cli

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"strings"

	"github.com/getbx/bx/internal/blink"
	"github.com/getbx/bx/internal/install"
	"github.com/urfave/cli/v2"
)

type uiServer struct {
	host      string
	sharesDir string
}

type shareView struct {
	Name   string `json:"name"`
	Listen string `json:"listen"`
	Status string `json:"status"`
}

type userView struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Listen string `json:"listen,omitempty"`
	Status string `json:"status"`
	Plan   string `json:"plan"`
}

func serverUIAction(c *cli.Context) error {
	if !isLoopbackListen(c.String("listen")) {
		return fmt.Errorf("server ui may only listen on a local address, 127.0.0.1:8787 for example")
	}
	s := uiServer{host: c.String("host"), sharesDir: c.String("shares-dir")}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/shares", s.handleShares)
	mux.HandleFunc("/api/share", s.handleShare)
	mux.HandleFunc("/api/revoke", s.handleRevoke)
	fmt.Printf("bx server ui: http://%s\n", c.String("listen"))
	fmt.Println("Tip: keep it on 127.0.0.1 and access it through SSH port forwarding.")
	return http.ListenAndServe(c.String("listen"), mux)
}

func isLoopbackListen(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s uiServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = uiTemplate.Execute(w, map[string]string{"Host": s.host})
}

func (s uiServer) handleShares(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	shares, err := s.shareViews()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeHTTPJSON(w, shares)
}

func (s uiServer) handleShare(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name, err := cleanShareName(r.FormValue("name"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// 主 server 是 reality → 多用户加 uuid;hys2 暂不支持;其余(brook)走多端口 share。
	if mainCfg, merr := readServerConfig(defaultServerConfigPath); merr == nil {
		switch mainCfg.Type {
		case "reality":
			rec, err := realityShare(name, s.sharesDir, mainCfg)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeHTTPJSON(w, map[string]string{"name": name, "type": "reality", "link": blink.Encode(rec.Link)})
			return
		case "hysteria2":
			http.Error(w, "a hysteria2 main server does not support multi-user shares yet", http.StatusBadRequest)
			return
		}
	}
	host := strings.TrimSpace(r.FormValue("host"))
	if host == "" {
		host = s.host
	}
	if host == "" {
		http.Error(w, "host is required", http.StatusBadRequest)
		return
	}
	link, listen, err := createShare(name, host, s.sharesDir, "", "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeHTTPJSON(w, map[string]string{"name": name, "listen": listen, "link": link})
}

func (s uiServer) handleRevoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name, err := cleanShareName(r.FormValue("name"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := revokeShare(name, s.sharesDir); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeHTTPJSON(w, map[string]string{"ok": "true"})
}

func (s uiServer) shareViews() ([]shareView, error) {
	shares, err := readShares(s.sharesDir)
	if err != nil {
		return nil, err
	}
	return shareViews(shares), nil
}

func shareViews(shares []shareInfo) []shareView {
	out := make([]shareView, 0, len(shares))
	for _, share := range shares {
		u := userViewFromShare(share)
		out = append(out, shareView{Name: u.Name, Listen: u.Listen, Status: u.Status})
	}
	return out
}

func userViews(shares []shareInfo) []userView {
	out := make([]userView, 0, len(shares))
	for _, share := range shares {
		out = append(out, userViewFromShare(share))
	}
	return out
}

func userViewFromShare(share shareInfo) userView {
	proto, _ := normalizeServerProtocol(share.Config.Type)
	status := "active"
	if proto == "brook" {
		status = serviceState("is-active", install.ShareServiceName(share.Name))
	}
	return userView{
		Name:   share.Name,
		Type:   proto,
		Listen: share.Config.Listen,
		Status: status,
		Plan:   "default",
	}
}

func writeHTTPJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

// 视觉与 Bx.app 菜单窗口、泄漏检测页同一套(2026-09-26):系统字体、浅灰底 + 白卡片、
// 深浅两套只换 token,品牌蓝只给唯一的主按钮。此前是一页通用的黑按钮后台,像别的产品。
// 行为一个字没改:三个 API、创建/撤销的流程都照旧。错误信息改用 textContent 写入 ——
// 它来自服务端的错误正文,此前用 innerHTML 拼进页面。
var uiTemplate = template.Must(template.New("ui").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>bx server</title>
<link rel="icon" href="data:image/png;base64,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">
<style>
:root{
  --ground:#F5F5F7; --card:#FFFFFF; --ink:#1D1D1F; --ink-dim:#6E6E73; --ink-faint:#AEAEB2;
  --rule:rgba(0,0,0,.08); --well:#F2F2F4; --field:#FFFFFF; --field-rule:rgba(0,0,0,.14);
  --ok:#1F8A3D; --ok-soft:rgba(52,199,89,.14);
  --bad:#D70015; --bad-soft:rgba(255,59,48,.12);
  --idle:#8E8E93; --idle-soft:rgba(142,142,147,.14);
  --accent:#2F6BFF; --accent-ink:#FFFFFF;
  --shadow:0 1px 2px rgba(0,0,0,.05),0 0 0 .5px rgba(0,0,0,.06);
}
@media (prefers-color-scheme: dark){
  :root{
    --ground:#1C1C1E; --card:#2C2C2E; --ink:#F5F5F7; --ink-dim:#A1A1A6; --ink-faint:#636366;
    --rule:rgba(255,255,255,.09); --well:#232325; --field:#1C1C1E; --field-rule:rgba(255,255,255,.14);
    --ok:#32D74B; --ok-soft:rgba(50,215,75,.16);
    --bad:#FF453A; --bad-soft:rgba(255,69,58,.16);
    --idle:#98989D; --idle-soft:rgba(152,152,157,.16);
    --accent:#4C82FF; --accent-ink:#FFFFFF;
    --shadow:0 0 0 .5px rgba(255,255,255,.08);
  }
}
*{box-sizing:border-box}
body{font:14px/1.5 -apple-system,BlinkMacSystemFont,"SF Pro Text","Segoe UI",system-ui,sans-serif;
  -webkit-font-smoothing:antialiased;background:var(--ground);color:var(--ink);margin:0;padding:44px 20px 72px}
main{max-width:44rem;margin:0 auto}
code,.mono{font-family:ui-monospace,"SF Mono",SFMono-Regular,Menlo,monospace;font-size:.8rem}
.brand{display:flex;align-items:center;gap:12px;margin:0 0 6px}
.brand img{width:36px;height:36px;border-radius:9px;box-shadow:var(--shadow)}
h1{font-size:1.45rem;font-weight:700;letter-spacing:-.02em;margin:0;line-height:1.15}
.sub{color:var(--ink-dim);margin:10px 0 26px;max-width:38rem}
.sect{margin:28px 0 8px;padding:0 4px}
.sect h2{font-size:.8rem;font-weight:600;color:var(--ink-dim);margin:0}
.card{background:var(--card);border-radius:12px;box-shadow:var(--shadow);overflow:hidden}
.form{padding:18px 20px 20px}
.fields{display:grid;grid-template-columns:1fr 1fr auto;gap:12px;align-items:end}
@media (max-width:560px){.fields{grid-template-columns:1fr}}
label{display:block;font-size:.8rem;color:var(--ink-dim);margin:0 0 4px}
input{font:inherit;width:100%;padding:7px 10px;border:1px solid var(--field-rule);border-radius:7px;background:var(--field);color:var(--ink)}
input:focus{outline:3px solid color-mix(in srgb,var(--accent) 35%,transparent);outline-offset:0;border-color:var(--accent)}
button{font:inherit;font-weight:600;border:0;border-radius:8px;padding:7px 16px;cursor:pointer}
button.primary{color:var(--accent-ink);background:var(--accent)}
button.primary:hover{filter:brightness(1.06)}
button.plain{color:var(--accent);background:transparent;padding:4px 8px}
button.danger{color:var(--bad);background:transparent;padding:4px 8px}
button:disabled{opacity:.45;cursor:default}
button:focus-visible{outline:3px solid color-mix(in srgb,var(--accent) 45%,transparent);outline-offset:2px}
.result{display:none;margin-top:16px}
.result.shown{display:block}
.link{display:flex;align-items:center;gap:8px;background:var(--well);border-radius:9px;padding:8px 8px 8px 12px}
.link code{flex:1;word-break:break-all;color:var(--ink)}
.msg{margin:10px 0 0;font-size:.86rem;color:var(--ink-dim);min-height:1.2em}
.msg.error{color:var(--bad)}
.fine{font-size:.8rem;color:var(--ink-faint);margin:10px 0 0}
.row{display:grid;grid-template-columns:1fr auto auto;gap:14px;align-items:center;padding:12px 16px;border-top:1px solid var(--rule)}
.row:first-child{border-top:0}
.row .name{font-weight:600}
.row .listen{color:var(--ink-dim)}
.pill{font-size:.7rem;font-weight:600;letter-spacing:.02em;text-transform:uppercase;padding:1px 7px;border-radius:5px;color:var(--idle);background:var(--idle-soft)}
.pill[data-tone="ok"]{color:var(--ok);background:var(--ok-soft)}
.pill[data-tone="bad"]{color:var(--bad);background:var(--bad-soft)}
.empty{padding:16px;color:var(--ink-dim)}
</style>
</head>
<body>
<main>
<div class="brand"><img src="data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAEAAAABACAYAAACqaXHeAAARJUlEQVR4nOWbeZTdVZHHP3Xvb32vtyQQiQMKQgBlDSAgizoOCogwSAQ3FI4IiLKJbAOoLIKARkc9npE/4AAKyuI4KjqRcTuAywwjISyKMxo4EoKTJkkn3W/5LffW/PF7r9OddKdfMsiEY71z3zunf/f3619VfatuVd26whSkqgKIiHiAPM8PF5FjnHdvFGEPYMB7ryIiU93/UpOqqjFGgHWqPGmNfUBVfxBF0YOd6wZQEdEN792IAVU1XcazMnu3Ffsx58rDojAWgKLM8d7/ZTnaQjLGEAYRAHmRqbXBQ07dV+Mgvgsm89alSQJQVSsibrgx/MrBePDm0IZHAWR5C1UcFSrMS8POlpGqekBFsHGUAlC4YvHabO1p29a3XdHlsTtfJtxoRcQ1Go3XR3H03cAG89pZywGIiH2pGXkxSFUdQBKntnTl83mW/329Xn94ohCkM9GIiG80Rg6MovriILCzmq1mCQQzmbmqYoxhK3EHU5JXX6ZxGpTOrcmzxlH1+tB/dHmWjoOg0WjMjeJwaRhEc1vtpovj2JoeFZ8XGaob+Zetirx6l8apLcpyZZ7l+9Tr9ZUAonq3FTnJtbPmj+IofdtYY7RMkiT447Knue3r3yIvcrzzZHm+kcc0xrD7brty8vtOIk0SnHdbNRJUtUziNMjy1v1JXDtS9W5befai/a4giO/J8napqkFRFLz7fafxwIO/oFavoV6nWC/AiGF0bIxzPnoG11/7adpZG2O2ah8JUMZREpRldmIYJveKqko7a/06idPXt9pNnySxfXb5Co56x7tot9tYa6eFt4iQ5wWzZg1x/w+/zZxZsyjKcmtHgUvi1LSz1sNJnB4ctFqtw+I42i/L24iIVa9YYwgCS1mW3ZumfJiI4L1DBMrSwVbMeJdExGZ5W6Mw2q/Vah1mrJXjjLFBZ/3szIJqNe19bM1a35BU1RtjA2vluMCrP6Tzd+l+q1cUj074TPuwzsyXGQmAV39IALJ3UeaIiJkE9Q21PB0pBMZSheIvD0GIiCnKHJC9A2tt39SxfW8SUBTvfcdPvHzMwHuPtbYvcM5Nm9X1AgAAr36rD4SmIuecBlMyL6CbYQI9SWkrJBGRYMor2v3pzQlu6vrWTpsI216+TG0OTY0A6MBa149p581wfSunaQWwuS7g5UqbQIBuMQKmWhG21khxegFsJjnncM7hvScMA0SqIomqoqrj16ASxtaSNb4oJoAIQ0ODRGFMGIaMjo6R5znOO4wY4jiiv78fGfe5Sjtr473/f68mbQIBvZmAAK4o+eZd97Js2TM886dnefbZ5TQbTZxzGGOo1+vMm7cdr371DizYZ28W7Ls3u+++K0YseZFRliXWblnZsVf/M52IpZ21xp+hqkRRxHMrVvC2o9/JyMhagiCYMcpTVRqNJs47rLWEQTCuWdUqVC7LEuc8YoRZQ4Ms2HcfTlx4PMcdezR99X7aWWu8vtgL06pgBWyPllROU8mfVgBvPfJ4Rtb1IIBOCmCNrcSsWoFmgm4EqUoFIqBQupJms4V3jr322oNzPnomJ514AkaEZqu5STR4hcBAGECrgJE26PpEFpHq1yt41mt+TqIYAbdBcWtKAaxYsYIjjjyekbW9IWBzSRDECCJCs9kkzwuOOvIIrrnqCnabP59WuzklErxCbKFRCj9+xvDwCljVWm8GgmAEBMUjHWaVwgn7bKecua/DMtlspgFQN7X9ywzF473HuZI0TRgaGmDx/T/m6HecwJ133U2a1MZXj4nMJwE83xSuechwx+PCsyNClkMrh2YGjUwZy5SxTGhmkOXQzgXn4DcrhOGmEJjJLm1KASgVfDYMBV7cUZmKc56ydAwNDtBstjjjI+dx/ecWkcTVrk53XmQrbV/3kOW/XxAGosr+vUgH+oIieK007zt6DC20Hew1F+bWlMJPrtxN60Je6oWpLB1BYBkaGuTqa2/kymuuJYlTVKsXLhQW/dry9GqohZX95w5KJ5Te4LzgPTivOK+UDrwTRlqw+zbKWfs5Atl41dhEHNBrJLAxTVzXN8d/eK+ICHNmz+LGRV8iSVIuvegCKBvc9GjEkj8LA7GSlRMcK77zK+OqVVUMMJorOwzBuQd46oGSOTAbaHZ6AfQYCXepWz733o9XiEQqR9f16t67GZ9VQd4za2gWV3/mBnbe6dVse+hC7nuqzWAcUPrKH6A6Dn0AER2HtjGGrFAGU7jwDY5tUqVZVsvmhrTpQKhHBKgqa9asxQYBtTQliiKMMaj3tLOMdevWYYzQ19eHtRbn3IzPQwx9fQkXf+oL7HHBIaSzX4HPSxTTidG0WuY6Eq0EUQmhdBBYuOBgx46DSrOYmvkZBNALCaqeKIo49+yzOPTQg9nuFXOppSlBEFCWjtGxUZ548il+8tOf89OfPcDatesYHBjAeV9Ba5oX814IrWdtI+LXd/2cgz5wLHGU4F3lFDyVQXvtaL6KyfEKpSqXHuzZd57SzKZnHqYNhJ7nLW89ZsZIUMRQlgWDgwP85t8fYHBgqPsmKErHP4/P/82SJXz2hkX86+J/q3ID6fqILgfdX4u4UcrktbTnnUleeOa8Zi77H/8mTFmt8evfSMe/1QvNEs45yLPwdZ5WvrHNb0j/x5Ss+ufee1atXkNR5jRbTVrtFlmW0Wq3aLWbNFsNWu0m+y9YwL3f+jpXffoysjwb9xPrTUwBg/gxyr79aM07HWcsYb9h+LkV/PZXS/FhSFl6nFO803GfYgXGCnjv3srCPXpjHmasCDGzC+hct9aO2/d6xzT5DZrtJgJc+PHz2GbOHM4570L6+vs6z1EQC66B69+P9t98CK8OpMQbIeiPeeaJ/8Img+ywxy5olhMEBoMSGxjL4aj5ng8v8GR577t00yJAN+PjfRW1zfQ/bSdBajTHOPWDJ3PF5Rezes2aagURi7oxfDqf9van4G2JWIeGggbgJSBI6zz92FOsXL6SMgjIS48ojGaw5yvg3IMcM7iW3gXQMymEYUAQ2J6ihe6y2Go3ueTCCzj9tFMZXjlMQAui7cle9WHKQMB0mRc0iMGmEPQhSZ0/Pr6cZiMjCAztArbth4sOddQCofS9ax82hYAeY1qvVVHDGNNz0NOND7I8Y9GN1/Ge93+YVc05FDufRR7VQQswBrUGtSlq6xDWIKxjk9kUvsbTvx0hdxCFwiWHe7YfhHbZm91PpI18gKoSWEsYhHjVGV0APVyfikQE5x1RGHHz167nf5I3cf/SBn1JQekMYg0SpGCSatgEbIo3KSbpZ7SZ8Lvfl9x6umG/VyqNdpUmby5tdIv3nrSW0tdXrxwaTI+AcQn0GC5OIFUlCkIazQarX/gzt1/3NnZ7VUqzXWKjAG9reFuHoA/CPgj78UE/avtRqRHECauGLT9aMg0jWyIAEcE5x0D/ADvssD1FUdB1J1Mmtt5jjWHtunUMv7AKI72ZQXeOGMP5F1zCQYcdSb5mOd++4Qhmzx4kp47EAxXDHaa97Qfbh5o6SIRXQxILX/me8vn7DWkCbgv6N6dEgBHDTjvtSOkcTJFBjTMC2CBgzZoRHnjwF4iYKsLbFPNUFeQkTrn2szdyx513Mzw8zAnvPY3XDOXccsVhaDiEhnVMPFAhwNZAUiAFQhSDV8WrEiVw2Z3CfY8LtS0QwrTIOej1+2NEZnSC6h1pEnPzLbcxsnaEJI6njfWd93jnqdf6uOW227n+hkXMGhpkcKCfpUse5YOnncOx+wzwxbPm03Z9SNiPBH1g64hNqjjBAFK1rzgFsQY1no/cZvj9C0IabZ4QNhKAMQbvHYe84SDmzduOLK+iiunqO84rSZry1O//wNnnfQIUamm9U/Fx40NVqSU1ammNL3/1nzj/gkuo1et4VfKiYPac2Xznu9/nksuv5Owj5nD5wtm0mzE2qIOJQSxigE6/c3etdwphYnluFZxxq2G02Ljqs1kC6C5PO2y/A2958xtpNBoTuj+mHs6VDA72c8+932Hhu9/PY48/Tpqk1NL6+AjDkEeWPMr7PnAqF196OVEUdnKBSptlWTJn9iwWffHLfPVrN/GZE7fh5L+1tMY8QeABB6KTaw0IRgTnoVaDB5bCJ+42RFEHoD0IYFIy1CXvPUmS8uBDv+Adx72LJI7xPYjUWsu6daMMDPRz4IEHsPeeezA4OMDK4WEee/xJlixZyujoKAMDAxvV/LrCV1Va7Zy777iZI445hsOvbPDwHzxpnyEvQ0yn9IV2Ui0DncSYQBytJnzuFOHCIzzN9sxl8ykF0BVCmqSc8qEzuPNb9zB79qxO29ymIg3FWktZOprN5nibHUAYhtRqaSdf6Lagbfisal+gKAqiKOGni/+Zvu334U1Xt3h2rSWJBedMFep1H0EnbBdFUIw61Av3XGA4bk8/Y3wgrXbTT9Ul4r0nimKWLVvGm//uaBqNJmEY4P3MSBBZX/YeZ61TLeoFl0EQMDo6yk6v2ZlHfvkjHllZ5+2LoF0KViq/4x147abbSqcMirXgcmXuoPDjS5XXzlWa+dRIUFU11k5dLjDGkGVt5u8yn+uuvYqxsbHOhufMDKgq3nlc6caHd70xXzVfeoqi4MSFx6G2xht2LPnSKb7a3ekwrZ2y2HhDY+fvzgtRanl+xHDarYY1eVUZnkpv1loxzrmx6bajrLU0201OOfn9XHTh+QwPD2Os7cQGL/5HjKAoq1av4rJ/uIgrP3k5Ip5GCz54gOeyYz2NMYeh7GSfHa66NXAMQrUPUK8rv/odnPkNC8F6nExUsHNuzIA+FgYRkzpFJ04UoZ21uO6aKzn7Yx9heOVwZwfGbGph2OwRWEtRFDTGGlx/3TVc9akraLWbqCrWCu1MuOLtnoWHKM21JZFto94jvtK+qCAKooJ3kGVCmpTc87OCa75vSeIOWABV9dXRGn3MGDG/7PA6JUC7dpwXOV/5x8/zycsvZd3oOrIsIwhs98jFFg8RIQgsI2vXEoURt95yExd/4uO02s3xrHG8YObgplPgwD0Cmg0IraIqVWG04wbUd2M0xTkhSoTr7lXu/E8hTaATqGqlXPNL45x+z3tXbuosUHd5amdtrr7yk9zx9VuYs80cXli1qtNwGHR2ZnpjG6oGCWsDsjxj+IVVHH7Yofzk/vt4z0kn0mw1NuobMFJthMxJlNvPFF41L6XVtsSBEFjBGO0MjxXFGhATEAQBQeg5/3bDk88LcQiKGO9d6Zx+b1K7fDtr+U2dD+p68lpaZ/lzz3HD577And+8m9WrV1Or1YjjGGsMSqWRiZAa37xAKYqSVrtFWZTsssvOnH/uxzj9tFMJw4hmq7HJ3WHnoZbAr54RTr7JsuxPvrP3XQVKdIusYqC7FSoKEvLDKxxHv847WN8uv9GBCXoolTvnSJIEI5Ynfvsk37jjm9z3g8Use/oZWq0WRgzWGsSYzla1ot7jXFU8mT17Fvst2IcT3nk87zz+WLbdZluyfH3HyEzkPaQxLF8L//Ko4U8vTJB2x15EQEy1bBZO2XGucOrBngDKaPKBiclHZtpZq5y2gXICrUdDDRBG1o3wyCOPsuTRpTzx5O947rkVNJvNysGFAf39/bx2913Ze6892X//Bey263yMWEpXkOc5xtjNKmV1t8rNZuxsZJmW8YZHZiYfmoqWhkEwt5W1nBHTU89KdxssDMPxQ4sAiqMoqkjQWoud9KYvTo+Q6tTr+4ZUodC7WpracsNDU9WDJhybi+uLA2tntbJWaaR3+WonP2fCnmCXsW7cP14IeYm7xLz3ZZpMc2xuAgN/lQcnx9UgIk5Vbb1ef3gkGzmgcMXiJE5tEqcWFFV10wVLWxOpqq8YV7rvX7hi8Ug2csCGzMMUqd1f9eHpLv01HZ//XwfNhdcL8bSwAAAAAElFTkSuQmCC" alt=""><h1>bx server</h1></div>
<p class="sub">Give each person their own link. Revoking one link does not affect anyone else. This page only listens on this machine — reach it through an SSH tunnel.</p>

<div class="sect"><h2>New link</h2></div>
<div class="card form">
  <div class="fields">
    <div><label for="name">Name</label><input id="name" placeholder="alice" autocomplete="off"></div>
    <div><label for="host">Server address</label><input id="host" value="{{.Host}}" placeholder="vps.example.com"></div>
    <button class="primary" id="create" onclick="createShare()">Create link</button>
  </div>
  <div class="result" id="result">
    <div class="link"><code id="link"></code><button class="plain" onclick="copyLink()">Copy</button></div>
    <p class="fine">This link is a credential. Send it privately; anyone who has it can use this server.</p>
  </div>
  <p class="msg" id="msg"></p>
</div>

<div class="sect"><h2>Links</h2></div>
<div class="card" id="shares"><div class="empty">Loading…</div></div>
</main>
<script>
async function api(path, options){const r=await fetch(path,options); if(!r.ok) throw new Error((await r.text()).trim()||('HTTP '+r.status)); return await r.json();}
function say(text, isError){const m=document.getElementById('msg'); m.textContent=text; m.className='msg'+(isError?' error':'');}
function tone(status){const s=(status||'').trim().toLowerCase(); if(s==='active'||s==='running') return 'ok'; if(/fail|error/.test(s)) return 'bad'; return '';}
async function loadShares(){
  const box=document.getElementById('shares');
  let rows;
  try{rows=await api('/api/shares');}catch(e){box.innerHTML=''; const d=document.createElement('div'); d.className='empty'; d.textContent='Could not load links: '+e.message; box.appendChild(d); return;}
  box.innerHTML='';
  if(rows.length===0){const d=document.createElement('div'); d.className='empty'; d.textContent='No links yet.'; box.appendChild(d); return;}
  for(const s of rows){
    const row=document.createElement('div'); row.className='row';
    const who=document.createElement('div');
    const n=document.createElement('div'); n.className='name'; n.textContent=s.name;
    const l=document.createElement('div'); l.className='listen mono'; l.textContent=s.listen;
    who.append(n,l);
    const p=document.createElement('span'); p.className='pill'; p.textContent=s.status||'unknown'; const t=tone(s.status); if(t) p.dataset.tone=t;
    const b=document.createElement('button'); b.className='danger'; b.textContent='Revoke'; b.onclick=()=>revokeShare(s.name);
    row.append(who,p,b); box.appendChild(row);
  }
}
async function createShare(){
  const btn=document.getElementById('create'); btn.disabled=true; say('');
  const fd=new FormData(); fd.set('name',document.getElementById('name').value); fd.set('host',document.getElementById('host').value);
  try{
    const r=await api('/api/share',{method:'POST',body:fd});
    document.getElementById('link').textContent=r.link;
    document.getElementById('result').classList.add('shown');
    say('Created '+r.name+' on '+r.listen+'.');
    await loadShares();
  }catch(e){say(e.message,true);}
  finally{btn.disabled=false;}
}
async function copyLink(){
  try{await navigator.clipboard.writeText(document.getElementById('link').textContent); say('Copied.');}
  catch(e){say('Could not copy — select the link and copy it yourself.',true);}
}
async function revokeShare(name){
  if(!confirm('Revoke '+name+'? Their link stops working right away.')) return;
  const fd=new FormData(); fd.set('name',name);
  try{await api('/api/revoke',{method:'POST',body:fd}); say('Revoked '+name+'.');}
  catch(e){say(e.message,true);}
  await loadShares();
}
loadShares();
</script>
</body>
</html>`))
