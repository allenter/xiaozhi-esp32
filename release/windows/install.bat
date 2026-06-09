@echo off
chcp 65001 >nul
title 喵小智 MCP 桥接服务器 - 安装

echo ========================================
echo   喵小智 MCP 桥接服务器 v2.0.0
echo   适用于 Windows
echo ========================================
echo.

REM Check if running as admin
net session >nul 2>&1
if %errorLevel% neq 0 (
    echo [警告] 建议以管理员身份运行此脚本（右键 → 以管理员身份运行）
    echo.
)

REM Create installation directory
set INSTALL_DIR=C:\Program Files\xiaozhi-bridge
if not exist "%INSTALL_DIR%" mkdir "%INSTALL_DIR%"

REM Copy the binary
echo [1/3] 复制程序文件到 %INSTALL_DIR% ...
copy /Y "%~dp0xiaozhi-bridge.exe" "%INSTALL_DIR%\" >nul
echo       完成

REM Create config
echo [2/3] 创建默认配置 ...
set PORT=8003
echo       端口: %PORT%

REM Install as Windows service using sc
echo [3/3] 注册为 Windows 服务 ...

REM Remove existing service if any
sc stop xiaozhi-bridge >nul 2>&1
sc delete xiaozhi-bridge >nul 2>&1

sc create xiaozhi-bridge ^
    binPath= "\"%INSTALL_DIR%\xiaozhi-bridge.exe\"" ^
    start= auto ^
    DisplayName= "喵小智 MCP 桥接服务器" ^
    obj= LocalSystem

if %errorLevel% equ 0 (
    echo       注册成功
    sc start xiaozhi-bridge >nul
    sc description xiaozhi-bridge "喵小智 MCP 桥接服务器 - 提供 ESP32 设备 WebSocket 接入"
) else (
    echo       注册失败 - 使用手动启动模式
    echo.
    echo 手动启动命令：
    echo   "%INSTALL_DIR%\xiaozhi-bridge.exe"
)

echo.
echo ========================================
echo   安装完成！
echo ========================================
echo.
echo 服务端口: %PORT%
echo 健康检查: http://localhost:%PORT%/health
echo OTA 端点: http://localhost:%PORT%/xiaozhi/ota/
echo WebSocket: ws://localhost:%PORT%/xiaozhi/v1
echo.
echo 如果防火墙提示，请允许 xiaozhi-bridge.exe 的网络访问。
echo.
pause
