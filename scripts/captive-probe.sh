#!/usr/bin/env bash
# 酒店/咖啡店 Wi-Fi 现场取证 —— **在 bx down 之前跑**,只读,不改任何东西。
#
# 它要回答两个今天只有推理、没有证据的问题:
#   ① bx 开着时,网关上的门户页够不够得着(私网恒直连,理论上够)?
#   ② 门户满足之后,隧道会不会自己回来(不用 down/up)?
#
# 用法:连上 Wi-Fi、发现上不了网之后,先跑这个,再照它最后的提示做。
set -u
out="${TMPDIR:-/tmp}/bx-captive-$(date +%Y%m%d-%H%M%S).txt"
exec > >(tee "$out") 2>&1

echo "=== 1. 物理网关与接口 ==="
route -n get default 2>/dev/null | sed -n 's/^[[:space:]]*\(gateway\|interface\):/\1:/p'
gw=$(route -n get default 2>/dev/null | awk '/gateway:/{print $2}')
echo "gateway=${gw:-<none>}"

echo
echo "=== 2. bx 怎么看这条路(只读) ==="
bx status 2>&1 | sed -n '1,40p'

echo
echo "=== 3. 网关上的门户页够不够得着(这一步回答问题①) ==="
if [ -n "${gw:-}" ]; then
  for scheme in http https; do
    code=$(curl -sS -m 5 -o /dev/null -w '%{http_code}' -k "$scheme://$gw/" 2>&1)
    echo "$scheme://$gw/ -> $code"
  done
else
  echo "拿不到网关,跳过"
fi

echo
echo "=== 4. macOS 自己的门户探测(境外域名,预期被 bx 挡住) ==="
curl -sS -m 5 -o /dev/null -w 'captive.apple.com -> %{http_code} (%{remote_ip})\n' \
  http://captive.apple.com/hotspot-detect.html 2>&1

echo
echo "=== 5. 最近的恢复事件 ==="
sudo tail -c 200000 /var/log/bx-guard.err.log 2>/dev/null | grep network_recovery | tail -5 \
  || echo "(读不到 guardian 日志,需要 sudo)"

cat <<'TIP'

────────────────────────────────────────────────────────────
接下来请**按这个顺序**做,每一步的结果都有用:

  A. 如果第 3 步有 200/302 之类的响应 —— 直接在浏览器打开那个
     http://<gateway>/,看门户页能不能完整加载并完成登录。
     **成了的话,你以后再也不用 bx down。** 这回答了问题①。

  B. 登录完成后,**先什么都别做,等 2 分钟**,再跑一次 `bx status`。
     隧道自己变健康 = 问题②的答案是「会自愈」,那条自动重试就不用做。
     两分钟后仍然不健康,再 bx down && bx up。

  C. 把这份记录发我:
TIP
echo "     $out"
