#!/bin/bash
set -e

PORT="${XIAOZHI_SERVER_PORT:-8003}"
echo "=== 喵小智 MCP 桥接服务器 v2.0.0 ==="
echo ""

if [ "$(id -u)" -ne 0 ]; then
    echo "请以 root 运行: sudo bash install.sh"
    exit 1
fi

# Download latest binary from GitHub Releases
BIN_URL="https://github.com/allenter/xiaozhi-esp32/releases/latest/download/xiaozhi-bridge"
if [ "$(uname -m)" = "x86_64" ]; then
    echo "[1/3] 下载 Linux x86_64 程序 ..."
    curl -sL "$BIN_URL" -o /opt/xiaozhi-bridge
else
    echo "[1/3] 不支持的架构: $(uname -m)"
    exit 1
fi
chmod +x /opt/xiaozhi-bridge

echo "[2/3] 创建 systemd 服务 ..."
cat > /etc/systemd/system/xiaozhi-bridge.service << EOF
[Unit]
Description=喵小智 MCP 桥接服务器
After=network.target

[Service]
Type=simple
ExecStart=/opt/xiaozhi-bridge
WorkingDirectory=/opt
Restart=always
RestartSec=3
Environment=XIAOZHI_SERVER_PORT=$PORT

[Install]
WantedBy=multi-user.target
EOF

echo "[3/3] 启动服务 ..."
systemctl daemon-reload
systemctl enable xiaozhi-bridge
systemctl restart xiaozhi-bridge

sleep 2
echo ""
echo "=== 安装完成 ==="
systemctl status xiaozhi-bridge --no-pager -l | head -10
echo ""
echo "端口: $PORT"
echo "健康检查: http://localhost:$PORT/health"
