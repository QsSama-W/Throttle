#!/bin/bash
set -e

INSTALL_DIR="/opt/throttle"
BIN_NAME="throttle"
CLI_NAME="xs"
LINK_PATH="/usr/local/bin/${CLI_NAME}"

REPO="QsSama-W/Throttle"
BRANCH="main"
BASE_URL="https://raw.githubusercontent.com/${REPO}/${BRANCH}"

info()  { printf "\033[1;32m[INFO]\033[0m  %s\n" "$1"; }
error() { printf "\033[1;31m[ERROR]\033[0m %s\n" "$1"; exit 1; }

[[ $EUID -ne 0 ]] && error "请用 root 运行: sudo bash install.sh"

# ========== 架构检测 ==========
ARCH=$(uname -m)
case "$ARCH" in
    x86_64)  FILE="throttle-linux-amd64" ;;
    aarch64) FILE="throttle-linux-arm64" ;;
    *)       error "不支持的架构: $ARCH" ;;
esac
info "架构: ${ARCH} → ${FILE}"

# ========== 下载 ==========
mkdir -p "${INSTALL_DIR}"

URL="${BASE_URL}/${FILE}"
info "下载: ${URL}"

if command -v curl &>/dev/null; then
    curl -fsSL "${URL}" -o "${INSTALL_DIR}/${BIN_NAME}"
elif command -v wget &>/dev/null; then
    wget -q "${URL}" -O "${INSTALL_DIR}/${BIN_NAME}"
else
    error "需要 curl 或 wget"
fi
chmod +x "${INSTALL_DIR}/${BIN_NAME}"

# ========== 检测出口网卡 ==========
DEFAULT_IFACE=$(ip route 2>/dev/null | awk '/^default/{print $5; exit}')
[[ -z "$DEFAULT_IFACE" ]] && DEFAULT_IFACE="eth0"

# ========== 交互式配置 ==========
echo ""
echo "========================================="
echo "        网络限速配置"
echo "========================================="
echo ""

read -rp "网卡名称 [${DEFAULT_IFACE}]: " INPUT_IFACE
IFACE="${INPUT_IFACE:-$DEFAULT_IFACE}"

read -rp "限速 Mbps [50]: " INPUT_LIMIT
LIMIT="${INPUT_LIMIT:-50}"

read -rp "突发大小 KB (0=自动) [0]: " INPUT_BURST
BURST="${INPUT_BURST:-0}"

# ========== 写入配置 ==========
CONFIG_PATH="${INSTALL_DIR}/config.json"
cat > "${CONFIG_PATH}" <<EOF
{
  "devices": [
    {
      "interface": "${IFACE}",
      "limit_mbps": ${LIMIT},
      "burst_kb": ${BURST}
    }
  ]
}
EOF

info "配置已写入: ${CONFIG_PATH}"

# ========== 创建 CLI ==========
cat > "${LINK_PATH}" <<'EOF'
#!/bin/bash
exec /opt/throttle/throttle "$@"
EOF
chmod +x "${LINK_PATH}"

# ========== 完成 ==========
echo ""
echo "========================================="
echo "  安装完成！"
echo "========================================="
echo ""
echo "  安装目录:  ${INSTALL_DIR}"
echo "  配置文件:  ${CONFIG_PATH}"
echo ""
echo "  终端输入 ${CLI_NAME} 启动控制面板"
echo ""
