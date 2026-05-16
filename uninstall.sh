#!/bin/sh
set -e

INSTALL_DIR="/opt/throttle"
CLI_PATH="/usr/local/bin/xs"

info()  { printf "\033[1;32m[INFO]\033[0m  %s\n" "$1"; }
error() { printf "\033[1;31m[ERROR]\033[0m %s\n" "$1"; exit 1; }

[ "$(id -u)" -ne 0 ] && error "请用 root 运行"

# ========== 移除开机自启 ==========
if [ -f "/etc/systemd/system/throttle.service" ]; then
    systemctl disable throttle.service 2>/dev/null || true
    rm -f /etc/systemd/system/throttle.service
    systemctl daemon-reload 2>/dev/null || true
    info "已移除 systemd 自启"
elif [ -f "/etc/init.d/throttle" ]; then
    rc-update del throttle default 2>/dev/null || true
    update-rc.d -f throttle remove 2>/dev/null || true
    rm -f /etc/init.d/throttle
    info "已移除 init.d 自启"
fi

# ========== 移除 tc 规则 ==========
if [ -f "${INSTALL_DIR}/config.json" ]; then
    for iface in $(grep -o '"interface": *"[^"]*"' "${INSTALL_DIR}/config.json" | sed 's/.*"//;s/"//'); do
        tc qdisc del dev "$iface" root 2>/dev/null || true
        info "已移除 ${iface} 的限速规则"
    done
fi

# ========== 删除文件 ==========
rm -rf "${INSTALL_DIR}"
info "已删除 ${INSTALL_DIR}"

rm -f "${CLI_PATH}"
info "已删除 ${CLI_PATH}"

echo ""
echo "========================================="
echo "  卸载完成！"
echo "========================================="
echo ""
