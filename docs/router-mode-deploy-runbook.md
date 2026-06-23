# Router-mode deployment runbook (GL.iNet Mudi 7)

For cutting bx router mode onto the Mudi **safely, with mihomo as a live fallback** and **real leak-verified** before committing. Authored 2026-06-22. Do each step; do not skip verification. LAN SSH is the control path (bypassed by bx).

> Context: an earlier host-mode attempt took LAN clients offline and killed the router's own Tailscale. Router mode fixes both (source-routed, router-own traffic untouched), but it MUST be verified from a real LAN client + leak test before declaring done — the host-mode failure was "declared done without a true LAN-client test."

## 0. Prereqs (local)
```sh
cd ~/Documents/bx && git checkout router-mode
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o /tmp/bx_arm64 .
cat /tmp/bx_arm64 | ssh root@192.168.8.1 'cat >/usr/bin/bx && chmod +x /usr/bin/bx && /usr/bin/bx --version'
```
`/etc/bx/config.yaml` on the Mudi (router mode):
```yaml
server: brook://server?server=203.0.113.10%3A9999&username&password=<pw>
mode: router
killswitch: true
http_proxy: 127.0.0.1:7890        # REQUIRED: tailscaled tunnels its control plane via
                                  # HTTP_PROXY=7890 (corp blocks controlplane direct); bx
                                  # provides it (drop-in for mihomo) so Tailscale stays online
router:
  lan_cidrs: [192.168.8.0/24]     # explicit; or omit to auto-detect br-* bridges
dns:
  split:
    - domains: ["*.shanghai-electric.com"]
      server: 10.0.13.23
bypass: [192.168.50.0/24, 10.20.0.0/24]   # admin/WG; LAN+CGNAT are auto-handled by PrivateCIDRs
```
Router mode proxies BOTH the router's own traffic AND LAN clients (catch-all pref 6600, after
Tailscale's 5210–5270), with v4+v6 fail-closed. The router's own traffic MUST be proxied here
because corp blocks its direct egress. Tailscale's 0x80000 transport still bypasses to direct.
Leave `/etc/init.d/tailscale`'s `HTTPS_PROXY=http://127.0.0.1:7890` as-is — bx now serves it.

## 1. Review the exact plan (no changes yet)
```sh
ssh root@192.168.8.1 'bx router-plan -c /etc/bx/config.yaml --tun bx0 --lan-ifaces br-lan'
```
Confirm: source rule `from 192.168.8.0/24 lookup 441`, `default dev bx0` + `blackhole default` (fail-closed), nft `LAN→bx0 accept` + `LAN ipv6 drop`. No surprises → proceed.

## 2. Stage rollback (one command restores mihomo)
The existing `/usr/bin/rollback-to-mihomo.sh` (stop+disable bx, dnsmasq→1053, un-disable hooks, restart mihomo). Re-stage if absent. Verify it's executable.

## 3. Cutover (mihomo stays installed as fallback)
```sh
ssh root@192.168.8.1 '
  # neutralize mihomo automation so it cannot fight bx
  crontab -l | grep -v mudi-vpn-health | crontab -
  mv /etc/hotplug.d/iface/99-vpn-mode /etc/hotplug.d/iface/99-vpn-mode.disabled 2>/dev/null
  mv /etc/hotplug.d/net/99-vpn-intent /etc/hotplug.d/net/99-vpn-intent.disabled 2>/dev/null
  /etc/init.d/mihomo stop
  # clean mihomo stale routing state (else its pref-6500→table-1001 rule shadows bx catch-all 6600)
  ip rule del from 192.168.8.0/24 lookup 1001 pref 6500 2>/dev/null; ip route flush table 1001 2>/dev/null
  # DNS → bx (both dnsmasq instances)
  for i in cfg01411c wgclient1; do uci -q delete dhcp.$i.server; uci add_list dhcp.$i.server=127.0.0.1#5354; uci set dhcp.$i.strictorder=1; done
  uci commit dhcp; /etc/init.d/dnsmasq restart
  /etc/init.d/bx enable; /etc/init.d/bx start; sleep 8
  bx status'
```

## 4. VERIFY (the step that was skipped before) — from a REAL LAN client (phone/laptop on the Mudi Wi-Fi)
- Browse a china-blocked site (google/youtube) → loads.
- `https://www.cloudflare.com/cdn-cgi/trace` → `ip=203.0.113.10` (VPS) for foreign; a CN site shows direct.
- **Leak test (CLI):** from the LAN client, `scripts/leak-test.sh 203.0.113.10` → IPv4 egress = VPS, no IPv6 egress, google → fake-IP. Then **browserleaks.com** (/ip /dns /webrtc) in a browser: public IP = VPS, no DNS leak, **no WebRTC IP**, no IPv6.
- On the router: `tailscale status` → gl-e5800 stays **online** (the host-mode failure mode — must NOT recur).
- Kill-switch: `ssh root@192.168.8.1 '/etc/init.d/bx stop'` → LAN client loses internet (no leak), `bx start` restores. Confirms fail-closed.

## 5. If anything is wrong → rollback immediately
```sh
ssh root@192.168.8.1 '/usr/bin/rollback-to-mihomo.sh'
```
Then diagnose offline; do not leave LAN clients degraded.

## 6. Persist across firmware (separate, after verified)
Update `provision-mudi.sh` in the `mudi7-smart-gateway` repo to install bx (binary→/usrdata, config, init.d, dnsmasq wiring, hook neutralization) so a firmware OTA rebuilds bx instead of mihomo. Until then, a firmware reset reverts to mihomo provisioning. (bx on `/usr/bin` survives reboot, not firmware.)

## Notes
- Router mode never hijacks the router's own traffic (source-based rule) → Tailscale/management/GL cloud unaffected by design.
- `udp.mode: proxy` (default) relays STUN/QUIC via the VPS → no WebRTC real-IP leak; `block` for max stealth.
- Transport is still brook plain TCP/9999 — see `docs/superpowers/specs/2026-06-22-transport-camouflage-design.md` for anti-DPI (separate, VPS-side).
