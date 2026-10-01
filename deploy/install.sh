#!/bin/sh
# Install/update published binaries; never overwrite subscription or UI rules.
set -eu
PATH=/opt/sbin:/opt/bin:/usr/sbin:/usr/bin:/sbin:/bin
export PATH
umask 077
REPO=Razlo-coder/xkeen-autofailover
dest=/opt/etc/xkeen-panel
bin=/opt/sbin/xkeen-panel
init=/opt/etc/init.d/S99xkeen-panel
[ "$(id -u)" = 0 ] || { echo 'Run as root.' >&2; exit 1; }
[ -f /opt/etc/init.d/rc.func ] && [ -x /opt/sbin/xkeen ] && [ -x /opt/sbin/xray ] || { echo 'Install Entware and XKeen with Xray first.' >&2; exit 1; }
command -v curl >/dev/null || { echo 'Install curl: opkg install curl' >&2; exit 1; }
case "${1:-$(uname -m)}" in
 aarch64|arm64) arch=aarch64 ;;
 armv7l|armv7|arm) arch=armv7 ;;
 mipsel|mipsle) arch=mipsel ;;
 mips) arch=mipsel ;; # Keenetic/Netcraze MIPS Entware uses little-endian binaries.
 *) echo 'Supported builds: aarch64, armv7, mipsel.' >&2; exit 1 ;;
esac
stage=$(mktemp -d /tmp/xkeen-autofailover-install.XXXXXX)
cleanup() { rm -f "$stage/panel" "$stage/SHA256SUMS" "$stage/checksum" "$stage/config.yaml"; rmdir "$stage" 2>/dev/null || true; }
trap cleanup EXIT HUP INT TERM
base="https://github.com/$REPO/releases/latest/download"
echo "Downloading xkeen-panel-$arch..."
curl -fL --retry 2 --connect-timeout 20 --max-time 300 "$base/xkeen-panel-$arch" -o "$stage/panel"
curl -fL --retry 2 --connect-timeout 20 --max-time 60 "$base/SHA256SUMS" -o "$stage/SHA256SUMS"
expected=$(awk -v file="xkeen-panel-$arch" '$2 == file { print $1 }' "$stage/SHA256SUMS")
[ ${#expected} = 64 ] || { echo 'Missing checksum.' >&2; exit 1; }
printf '%s  %s\n' "$expected" "$stage/panel" > "$stage/checksum"
sha256sum -c "$stage/checksum"
chmod 700 "$stage/panel"
[ ! -f "$dest/data/failover-pending.json" ] || { echo 'Recover the pending failover transaction before updating.' >&2; exit 1; }
mkdir -p "$dest/data" "$dest/backup"
chmod 700 "$dest" "$dest/data" "$dest/backup"
if [ -f "$dest/config.yaml" ]; then
 cp -p "$dest/config.yaml" "$stage/config.yaml"
 # A pre-fork installation lacks this mode. Install its defaults without
 # touching paths, credentials, the existing outbound or the user's UI rules.
 if ! grep -q '^verified_failover:' "$stage/config.yaml"; then
  cat >> "$stage/config.yaml" <<'POLICY'

verified_failover:
  enabled: true
  country_priority: []
  allow_other_countries: true
  bypass_mark: 255
  probe_timeout_sec: 8
  retry_interval_sec: 120
POLICY
 fi
else
 cat > "$stage/config.yaml" <<'CONFIG'
port: 3000
data_dir: /opt/etc/xkeen-panel/data
xkeen_path: /opt/sbin/xkeen
outbounds_file: /opt/etc/xray/configs/04_outbounds.json
check_interval: 60
max_fails: 3
watchdog_auto_start: true
latency_auto_switch: false
subscription_refresh_interval: 1800
blacklist_ttl_sec: 300
log_file: /tmp/xkeen-panel.log
health_check_urls:
  - https://www.cloudflare.com/cdn-cgi/trace
  - https://www.youtube.com/generate_204
verified_failover:
  enabled: true
  country_priority: []
  allow_other_countries: true
  bypass_mark: 255
  probe_timeout_sec: 8
  retry_interval_sec: 120
CONFIG
fi
"$stage/panel" -config "$stage/config.yaml" -preflight
backup="$dest/backup/update-$(date +%Y%m%d-%H%M%S)"
mkdir -p "$backup"
for name in "$bin" "$init" "$dest/config.yaml"; do [ ! -f "$name" ] || cp -p "$name" "$backup/$(basename "$name")"; done
[ ! -x "$init" ] || "$init" stop
cp "$stage/panel" "$bin.new"; chmod 700 "$bin.new"; mv "$bin.new" "$bin"
cp "$stage/config.yaml" "$dest/config.yaml.new"; mv "$dest/config.yaml.new" "$dest/config.yaml"
cat > "$init" <<'INIT'
#!/bin/sh
ENABLED=yes
PROCS="xkeen-panel"
ARGS="-config /opt/etc/xkeen-panel/config.yaml"
DESC="XKeen AutoFailover"
PREARGS=""
. /opt/etc/init.d/rc.func
INIT
chmod 700 "$init"
sync
port=$(awk '/^port:/{print $2;exit}' "$dest/config.yaml")
port=${port:-3000}
started=false
if "$init" start; then
 sleep 3
 if curl --noproxy '*' -fsS --max-time 10 "http://127.0.0.1:$port/api/auth/status" >/dev/null; then started=true; fi
fi
if [ "$started" != true ]; then
 echo "Panel did not start. Restoring previous installation: $backup" >&2
 "$init" stop || true
 if [ -f "$backup/xkeen-panel" ]; then cp -p "$backup/xkeen-panel" "$bin"; else rm -f "$bin"; fi
 if [ -f "$backup/config.yaml" ]; then cp -p "$backup/config.yaml" "$dest/config.yaml"; else rm -f "$dest/config.yaml"; fi
 if [ -f "$backup/S99xkeen-panel" ]; then cp -p "$backup/S99xkeen-panel" "$init"; "$init" start; else rm -f "$init"; fi
 exit 1
fi
echo "Installed. Open http://<router-address>:$port and set your subscription and priorities."
echo "Backup: $backup"
