# 改版说明

本文档记录在官方 V2 版源码（[78/xiaozhi-esp32](https://github.com/78/xiaozhi-esp32)）基础上所做的定制修改。

最后更新：2026-06-09

---

## 一、固件改动

### 1. 启动流程改造：移除远程 OTA 检查，改为候选服务器轮询

**问题：** 原版固件开机后调用远程 OTA 接口检查新版本，网络不通时反复重试，导致卡在"检测新版本"无法进入待命状态。

**修改：** 彻底移除 `CheckNewVersion()` 的调用，改为在 `ActivationTask()` 中按优先级轮询候选服务器。

**文件：** `main/application.cc` → `ActivationTask()` 函数

**逻辑：**

```
Step 1: 读取 WiFi 配网页（192.168.4.1）中用户自定义的 OTA URL
   ↓ 有自定义URL → 解析 host:port → 探测 /health
   ↓ 通 → 使用它；不通 → 进入 Step 2

Step 2: 按顺序探测内置候选服务器
   1. http://192.168.0.88:8003/health     → ws://192.168.0.88:8003/xiaozhi/v1
   2. http://hass.iala.top:8003/health    → ws://hass.iala.top:8003/xiaozhi/v1
   3. https://api.tenclass.net/xiaozhi/ota/ → 解析返回的 wss:// 地址（带 token）

Step 3: 全部不通 → 默认用 ws://192.168.0.88:8003/xiaozhi/v1
```

每个候选只探测一次（HTTP GET `/health`），不重试。如果连接 `xiaozhi.me` 云端且有激活码，会调用 `ShowActivationCode()` 语音播报。

### 2. 中间按钮：待命 ↔ 对话状态切换

**问题：** 原版按钮在"连接中"状态无法取消，一直卡住。

**修改：** `HandleToggleChatEvent()` 增加 `kDeviceStateConnecting` 处理分支。按下按钮时：

| 当前状态 | 切换到 |
|---------|--------|
| 待命 | 连接 → 对话 |
| 连接中 | 取消连接 → 待命 |
| 对话/聆听/说话 | 关闭对话 → 待命 |
| 正在激活 | 待命 |

**文件：** `main/application.cc` → `HandleToggleChatEvent()`

### 3. 添加微信绑定二维码功能

**新文件：**
- `main/qrcode/qrcodegen.c` / `.h` — Nayuki QR Code 生成库 (MIT)
- `main/qrcode/qrcode_display.cc` / `.h` — LVGL 屏幕二维码显示模块

**功能：** ESP32 收到 `{"type":"bind_qr","url":"https://..."}` 消息后，在屏幕上显示 120 秒的微信绑定二维码。

**触发方式：** 桥接服务器检测到 OpenClaw 智能体回复 `!bind` 时，自动生成绑定 URL 并 push `bind_qr` 到设备。

**文件修改：** `main/application.cc` → `OnIncomingJson()` 回调，新增 `bind_qr` 类型处理；`main/CMakeLists.txt` 添加 qrcode 源文件。

### 4. 启动时显示设备注册码

开机进入待命状态后，屏幕显示设备 MAC 地址作为注册码：`Code: XX:XX:XX:XX:XX:XX`

### 5. 配网页 OTA 地址支持

WiFi 配网页（192.168.4.1）的 OTA 地址字段可以填入任意 MCP 服务器的 URL，固件优先使用该地址。例如：

- `http://hass.iala.top:8003/xiaozhi/ota/` — 连本地/远程桥接服务器
- `https://api.tenclass.net/xiaozhi/ota/` — 连 xiaozhi.me 官方服务器

### 6. 编译期配置

**文件：** `sdkconfig.defaults`

```ini
CONFIG_OTA_URL="http://192.168.0.148:8003/xiaozhi/ota/"
```

**文件：** `main/Kconfig.projbuild` — 新增 `CONFIG_WEBSOCKET_URL` 配置项。

---

## 二、MCP 桥接服务器

### 概述

为 ESP32 设备提供 WebSocket 接入、消息路由、微信绑定等功能的独立服务器，Go 语言编写，静态编译，无需依赖。

### 仓库地址

**源码：** https://github.com/allenter/xiaozhi-esp32（`main` 分支）

**Release 下载：** https://github.com/allenter/xiaozhi-esp32/releases

| 文件 | 平台 | 说明 |
|------|------|------|
| `xiaozhi-bridge` | Linux x86_64 | Debian/Ubuntu 服务器 |
| `xiaozhi-bridge.exe` | Windows x86_64 | Windows 10/11 |

### API 端点

| 端点 | 方法 | 说明 |
|------|------|------|
| `/health` | GET | 健康检查 |
| `/xiaozhi/ota/` | GET/POST | OTA 配置下发（返回 WebSocket URL + 服务器时间） |
| `/xiaozhi/updates?device_id=X&offset=N&timeout=T` | GET | 长轮询获取设备消息（供 OpenClaw 插件使用） |
| `/xiaozhi/reply` | POST | 将 OpenClaw 智能体回复推送给设备 |
| `/xiaozhi/bind` | POST | 触发设备绑定二维码 |
| `/xiaozhi/bind/confirm` | GET | 用户扫码确认绑定页面 |
| `/xiaozhi/bind/status` | GET | 查询设备绑定状态 |
| `/xiaozhi/v1` | WebSocket | ESP32 设备 WebSocket 直连 |

### !bind 指令

当 OpenClaw 智能体回复的消息内容恰好为 `!bind` 时，桥接服务器自动：

1. 生成随机 token + 绑定 URL
2. 向 ESP32 推送 `{"type":"bind_qr","url":"..."}` → 屏幕弹出微信二维码
3. 同时推送文本回复 "请在设备屏幕查看微信绑定二维码"

使用方式：对 ESP32 说「微信绑定」，OpenClaw 智能体回复 `!bind`。

### 部署

**已有服务器：** `hass.iala.top:8003`（已部署运行）

**Linux 一键部署：**
```bash
wget https://github.com/allenter/xiaozhi-esp32/releases/download/v2.0.0/xiaozhi-bridge -O /opt/xiaozhi-bridge
chmod +x /opt/xiaozhi-bridge
/opt/xiaozhi-bridge
```

**Windows：** 下载 `xiaozhi-bridge.exe`，右键 `install.bat` 以管理员身份运行。

**systemd 服务（Linux）：**
详见仓库中 `release/linux/install.sh`。

---

## 三、OpenClaw 配置修改

### xiaozhi 渠道

```json
{
  "channels": {
    "xiaozhi": {
      "enabled": true,
      "xiaozhiServerUrl": "http://hass.iala.top:8003",
      "deviceId": "10:20:ba:6c:d2:40",
      "timeout": 30
    }
  }
}
```

### Main Agent 系统提示词

增加了微信绑定指令处理：当用户说"微信绑定"等触发词时，智能体只回复 `!bind`，触发设备显示二维码。

---

## 四、完整链路

```
用户对ESP32说话 "微信绑定"
  → ESP32 WebSocket → hass.iala.top:8003 (桥接)
  → 插件长轮询 /xiaozhi/updates → OpenClaw Main Agent
  → 智能体识别意图 → 回复 "!bind"
  → 桥接检测到 !bind → 生成绑定URL → push bind_qr 到 ESP32
  → ESP32 屏幕显示微信二维码（120秒）
  → 用户扫码 → 绑定确认页 → ✅ 绑定成功
```

正常对话流程：
```
用户说话 → ESP32 → 桥接 → 插件 → OpenClaw → 回复 → 桥接 → ESP32 播放
```

---

## 五、兼容性说明

- 本固件基于官方 V2 版源码，仅修改 `main/application.cc`、`main/CMakeLists.txt`、`main/Kconfig.projbuild`、`sdkconfig.defaults`，新增 `main/qrcode/` 目录
- 除 xingzhi-cube-1.54tft-wifi 以外的所有官方板子配置均未修改
- `78/esp-ml307` 组件的 ESP-IDF 版本要求从 `>=5.5.2` 改为 `>=5.5`（因编译环境限制）
- `78__uart-uhci` 组件中 `gdma_link_get_length` 替换为兼容 ESP-IDF v5.5 的实现

---

## 六、编译

```bash
source ~/esp-idf/export.sh
cd xiaozhi-esp32
idf.py set-target esp32s3
idf.py build
```

**要求：** ESP-IDF v5.5+，已安装 ESP32-S3 工具链。

**合并固件：**
```bash
esptool.py --chip esp32s3 merge_bin -o xingzhi-cube-merged.bin \
  --flash_mode dio --flash_size 16MB --flash_freq 80m \
  0x0 build/bootloader/bootloader.bin \
  0x8000 build/partition_table/partition-table.bin \
  0xd000 build/ota_data_initial.bin \
  0x20000 build/xiaozhi.bin \
  0x800000 build/generated_assets.bin
```

---

## License

与原项目一致，MIT License。
