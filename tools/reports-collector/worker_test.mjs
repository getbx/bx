// 收集端纯函数的断言。判据是退出码(node --test)。
import { test } from "node:test";
import assert from "node:assert/strict";
import { validate, issueTitle, signatureLabel, renderIssueBody, rateKeys, MAX_BYTES } from "./worker.js";

const good = {
  schema: 1, install_id: "0123456789abcdef0123456789abcdef", bx_version: "v0.4.16", os: "darwin", arch: "arm64",
  occurred_at: "2026-09-29T01:02:03Z", signature: "attention:core_health_failed",
  failure: { code: "core_health_failed", stage: "transport_health", error_code: "transport_unavailable" },
  protection: [{ at: "2026-09-29T01:00:00Z", state: "protected" }, { at: "2026-09-29T01:02:00Z", state: "needs_attention", code: "core_health_failed" }],
  doctor: { checks: [{ name: "tunnel", status: "fail", detail: "could not reach <ip-1>" }] },
  log_tail: ["2026/09/29 01:02:03 guardian_needs_attention code=core_health_failed"],
};

test("validate accepts the shape the client sends and rejects everything else", () => {
  assert.equal(validate(good), null);
  assert.match(validate({ ...good, schema: 2 }), /schema/);
  assert.match(validate({ ...good, install_id: "short" }), /install_id/);
  assert.match(validate({ ...good, signature: "has space" }), /signature/);
  assert.match(validate({ ...good, occurred_at: "yesterday" }), /occurred_at/);
  assert.match(validate({ ...good, log_tail: "x" }), /log_tail/);
  assert.match(validate([]), /object/);
  assert.match(validate(null), /object/);
});

test("issue title and label come from the signature; same signature merges", () => {
  assert.equal(issueTitle(good), "[attention:core_health_failed] bx v0.4.16 on darwin");
  assert.equal(signatureLabel(good.signature), "code:attention");
  assert.equal(signatureLabel("pf_residue"), "code:pf_residue");
});

test("issue body renders only known fields and keeps the redacted placeholders as-is", () => {
  const body = renderIssueBody(good, "report:x");
  assert.match(body, /code: core_health_failed/);
  assert.match(body, /\| tunnel \| fail \| could not reach <ip-1> \|/);
  assert.match(body, /Guardian log tail \(1 lines\)/);
  assert.doesNotMatch(body, /0123456789abcdef0123456789abcdef/, "the full install id is not printed, only a prefix");
});

test("rate keys bucket by day, install and ip", () => {
  const k = rateKeys(good, "203.0.113.5", Date.parse("2026-09-29T23:59:00Z"));
  assert.equal(k.install, "rate:install:0123456789abcdef0123456789abcdef:2026-09-29");
  assert.equal(k.ip, "rate:ip:203.0.113.5:2026-09-29");
  assert.equal(rateKeys(good, "", 0).ip, "rate:ip:unknown:1970-01-01");
});

test("the size cap is 64 KiB", () => {
  assert.equal(MAX_BYTES, 65536);
});
