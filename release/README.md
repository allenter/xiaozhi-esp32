# 喵小智 MCP 桥接服务器

适用于 ESP32 小智设备的 WebSocket 桥接服务器，提供设备接入、OTA 配置下发、微信绑定二维码等功能。

## 功能

- **设备 WebSocket 接入** — ESP32 通过 `ws://host:8003/xiaozhi/v1` 直连
- **OTA 配置下发** — `/xiaozhi/ota/` 返回 WebSocket URL 和服务器时间
- **微信绑定** — 智能体回复 `!bind` 触发设备屏幕显示微信二维码
- **长轮询接口** — `/xiaozhi/updates` 供 OpenClaw 插件使用
- **回复接口** — `/xiaozhi/reply` 将智能体回复推送给设备

## API 端点

| 端点 | 方法 | 说明 |
|------|------|------|
| `/health` | GET | 健康检查 |
| `/xiaozhi/ota/` | GET/POST | OTA 配置下发 |
| `/xiaozhi/updates?device_id=X&offset=N&timeout=T` | GET | 长轮询获取设备消息 |
| `/xiaozhi/reply` | POST | 向设备推送回复 |
| `/xiaozhi/bind` | POST | 触发设备绑定二维码 |
| `/xiaozhi/bind/confirm?device=X&token=Y` | GET | 用户扫码确认绑定 |
| `/xiaozhi/bind/status?device_id=X` | GET | 查询绑定状态 |
| `/xiaozhi/v1?device_id=X` | WS | ESP32 设备 WebSocket 连接 |

## 快速部署

### Windows
1. 解压 `windows.zip`
2. 右键 `install.bat` → **以管理员身份运行**
3. 如提示防火墙，允许 `xiaozhi-bridge.exe` 的网络访问

### Linux (Debian/Ubuntu)
1. 上传 `xiaozhi-bridge` 到 `/opt/`
2. 运行一键安装：
```bash
chmod +x install.sh && ./install.sh
```

### 手动运行
```bash
# Windows 命令行
xiaozhi-bridge.exe

# Linux
./xiaozhi-bridge
```

服务默认监听 **8003** 端口，可通过环境变量 `XIAOZHI_SERVER_PORT` 修改。

## 绑定二维码

当 OpenClaw 智能体回复 `!bind` 时，服务器自动生成二维码 URL 推送到 ESP32 屏幕：

```
用户说"微信绑定" → OpenClaw 智能体回复 "!bind" → 桥接推送 bind_qr → ESP32 显示二维码
```

## 编译

```bash
# Linux
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o xiaozhi-bridge .

# Windows
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o xiaozhi-bridge.exe .
```

## License

MIT
