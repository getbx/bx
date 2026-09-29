// bx 问题上报的收集端(Cloudflare Worker)。
// 设计:docs/superpowers/specs/2026-09-29-bx-problem-reports-design.md
//
// 客户端(Guardian)在几类失败上把一份**脱敏**包 POST 到 /v1/reports;这里校验、限频、
// 存 KV,有 GITHUB_TOKEN 时按失败签名归并成私有仓库的 issue。GitHub 那一跳失败不影响
// 202:报告已经在 KV 里,下一份来时再试。没有 token 也能跑(只存 KV)。
//
// 纯函数(validate / issueTitle / renderIssueBody / rateKeys)导出给 node 测试
// (worker_test.mjs);fetch 只做接线。

export const MAX_BYTES = 64 * 1024;
export const PER_INSTALL_PER_DAY = 20;
export const PER_IP_PER_DAY = 20;
export const KV_TTL_SECONDS = 90 * 24 * 3600;

const REQUIRED = ["schema", "install_id", "bx_version", "os", "occurred_at", "signature"];

// validate 只认形状:必填字段在、类型对、签名与 install_id 长得像我们发的。
// 返回 null 表示通过,否则是一句给 400 用的话(不回显任何用户内容)。
export function validate(body) {
  if (!body || typeof body !== "object" || Array.isArray(body)) return "body must be a JSON object";
  if (body.schema !== 1) return "unsupported schema";
  for (const k of REQUIRED) {
    if (typeof body[k] !== "string" && k !== "schema") return `missing ${k}`;
  }
  if (!/^[0-9a-f]{32}$/.test(body.install_id)) return "install_id must be 32 hex chars";
  if (!/^[a-z0-9_:.-]{1,120}$/.test(body.signature)) return "signature must be [a-z0-9_:.-]{1,120}";
  if (Number.isNaN(Date.parse(body.occurred_at))) return "occurred_at must be RFC 3339";
  if (body.log_tail !== undefined && !Array.isArray(body.log_tail)) return "log_tail must be an array";
  return null;
}

// issueTitle:`[signature] bx <version> on <os>`;同一签名归并到同一条 issue。
export function issueTitle(report) {
  return `[${report.signature}] bx ${report.bx_version} on ${report.os}`;
}

// signatureLabel:签名首段当标签(attention / corestart / recovery / update / pf_residue)。
export function signatureLabel(signature) {
  return "code:" + String(signature).split(":")[0];
}

// renderIssueBody:可读渲染。**只渲染我们知道的字段**,不把整个 JSON 原样贴进去 ——
// 客户端已经脱敏,但收集端不该成为「什么都往 issue 里塞」的那一层。
export function renderIssueBody(report, kvKey) {
  const lines = [];
  lines.push(`**bx** ${report.bx_version} · **os** ${report.os}${report.arch ? " " + report.arch : ""}${report.macos_version ? " (" + report.macos_version + ")" : ""}`);
  lines.push(`**signature** \`${report.signature}\` · **occurred_at** ${report.occurred_at} · **install** \`${report.install_id.slice(0, 8)}…\``);
  if (report.failure && typeof report.failure === "object") {
    const f = report.failure;
    lines.push("", "### Failure", "", "```", `code: ${f.code || ""}`, `stage: ${f.stage || ""}`, `error_code: ${f.error_code || ""}`, "```");
  }
  if (Array.isArray(report.protection) && report.protection.length) {
    lines.push("", "### Protection transitions", "", "```");
    for (const p of report.protection.slice(-8)) lines.push(`${p.at || ""}  ${p.state || ""}${p.code ? "  " + p.code : ""}`);
    lines.push("```");
  }
  if (report.doctor && Array.isArray(report.doctor.checks)) {
    lines.push("", "### Doctor", "", "| check | status | detail |", "|---|---|---|");
    for (const c of report.doctor.checks) lines.push(`| ${c.name} | ${c.status} | ${String(c.detail || "").replace(/\|/g, "\\|")} |`);
  }
  if (Array.isArray(report.log_tail) && report.log_tail.length) {
    lines.push("", "<details><summary>Guardian log tail (" + report.log_tail.length + " lines)</summary>", "", "```");
    for (const l of report.log_tail.slice(-200)) lines.push(String(l));
    lines.push("```", "</details>");
  }
  lines.push("", `_stored as \`${kvKey}\`_`);
  return lines.join("\n");
}

// rateKeys:一天一个桶,按 install_id 与来源 IP 各一个。
export function rateKeys(report, ip, now) {
  const day = new Date(now).toISOString().slice(0, 10);
  return { install: `rate:install:${report.install_id}:${day}`, ip: `rate:ip:${ip || "unknown"}:${day}` };
}

async function bump(kv, key, limit) {
  const cur = parseInt((await kv.get(key)) || "0", 10);
  if (cur >= limit) return false;
  await kv.put(key, String(cur + 1), { expirationTtl: 2 * 24 * 3600 });
  return true;
}

async function github(env, method, path, body) {
  const r = await fetch("https://api.github.com" + path, {
    method,
    headers: {
      Authorization: "Bearer " + env.GITHUB_TOKEN,
      Accept: "application/vnd.github+json",
      "User-Agent": "bx-reports-collector",
      "Content-Type": "application/json",
    },
    body: body ? JSON.stringify(body) : undefined,
  });
  if (!r.ok) throw new Error("github " + method + " " + path + ": " + r.status);
  return r.json();
}

// fileIssue:签名 → issue 号住在 KV(issue:<signature>);有就评论,没有就建。
// 建好之后把 issue 号写回 KV;评论时顺手把标题里的计数 +1(标题形如 `… (×3)`)。
async function fileIssue(env, report, kvKey) {
  const repo = env.GITHUB_REPO || "getbx/bx-reports";
  const issueKey = "issue:" + report.signature;
  const existing = await env.REPORTS.get(issueKey);
  const body = renderIssueBody(report, kvKey);
  if (existing) {
    const n = parseInt(existing, 10);
    await github(env, "POST", `/repos/${repo}/issues/${n}/comments`, { body });
    const count = parseInt((await env.REPORTS.get(issueKey + ":count")) || "1", 10) + 1;
    await env.REPORTS.put(issueKey + ":count", String(count));
    await github(env, "PATCH", `/repos/${repo}/issues/${n}`, { title: issueTitle(report) + ` (×${count})` });
    return n;
  }
  const created = await github(env, "POST", `/repos/${repo}/issues`, {
    title: issueTitle(report),
    body,
    labels: ["auto", signatureLabel(report.signature)],
  });
  await env.REPORTS.put(issueKey, String(created.number));
  await env.REPORTS.put(issueKey + ":count", "1");
  return created.number;
}

export default {
  async fetch(request, env, ctx) {
    const url = new URL(request.url);
    if (url.pathname === "/healthz") return new Response("ok", { status: 200 });
    if (url.pathname !== "/v1/reports") return new Response("not found", { status: 404 });
    if (request.method !== "POST") return new Response("method not allowed", { status: 405 });
    const len = parseInt(request.headers.get("content-length") || "0", 10);
    if (len > MAX_BYTES) return new Response("too large", { status: 413 });
    const raw = await request.text();
    if (raw.length > MAX_BYTES) return new Response("too large", { status: 413 });
    let report;
    try {
      report = JSON.parse(raw);
    } catch {
      return new Response("bad json", { status: 400 });
    }
    const problem = validate(report);
    if (problem) return new Response(problem, { status: 400 });

    const ip = request.headers.get("cf-connecting-ip") || "";
    const keys = rateKeys(report, ip, Date.now());
    if (!(await bump(env.REPORTS, keys.install, PER_INSTALL_PER_DAY))) return new Response("rate limited", { status: 429 });
    if (!(await bump(env.REPORTS, keys.ip, PER_IP_PER_DAY))) return new Response("rate limited", { status: 429 });

    const kvKey = `report:${new Date().toISOString()}:${report.install_id}`;
    await env.REPORTS.put(kvKey, raw, { expirationTtl: KV_TTL_SECONDS });

    if (env.GITHUB_TOKEN) {
      // 不让 GitHub 的慢或挂拖住 202:报告已经落了 KV。
      ctx.waitUntil(fileIssue(env, report, kvKey).catch((e) => console.log("github failed: " + e.message)));
    }
    return new Response("accepted", { status: 202 });
  },
};
