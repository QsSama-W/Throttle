#!/bin/sh
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

[ "$(id -u)" -ne 0 ] && error "请用 root 运行: wget -O- URL | sh"

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

if command -v curl >/dev/null 2>&1; then
    curl -fsSL "${URL}" -o "${INSTALL_DIR}/${BIN_NAME}"
elif command -v wget >/dev/null 2>&1; then
    wget -q "${URL}" -O "${INSTALL_DIR}/${BIN_NAME}"
else
    error "需要 curl 或 wget"
fi
chmod +x "${INSTALL_DIR}/${BIN_NAME}"

# ========== 检测出口网卡 ==========
DEFAULT_IFACE=$(ip route 2>/dev/null | awk '/^default/{print $5; exit}')
if [ -z "$DEFAULT_IFACE" ]; then
    DEFAULT_IFACE="eth0"
fi

# ========== 交互式配置 ==========
echo ""
echo "========================================="
echo "        网络限速配置"
echo "========================================="
echo ""

printf "网卡名称 [%s]: " "$DEFAULT_IFACE"
read -r INPUT_IFACE </dev/tty || true
IFACE="${INPUT_IFACE:-$DEFAULT_IFACE}"

printf "限速 Mbps [50]: "
read -r INPUT_LIMIT </dev/tty || true
LIMIT="${INPUT_LIMIT:-50}"

printf "突发大小 KB (0=自动) [0]: "
read -r INPUT_BURST </dev/tty || true
BURST="${INPUT_BURST:-0}"

# ========== 写入配置文件 ==========
CONFIG_PATH="${INSTALL_DIR}/config.json"
printf '{\n  "devices": [\n    {\n      "interface": "%s",\n      "limit_mbps": %s,\n      "burst_kb": %s\n    }\n  ]\n}\n' \
    "$IFACE" "$LIMIT" "$BURST" > "${CONFIG_PATH}"

info "配置已写入: ${CONFIG_PATH}"

# ========== 创建 CLI 快捷命令 ==========
printf '#!/bin/sh\nexec /opt/throttle/throttle "$@"\n' > "${LINK_PATH}"
chmod +x "${LINK_PATH}"

info "CLI 命令已创建: ${LINK_PATH}"

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
