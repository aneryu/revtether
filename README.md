# Reverse Tether

让 Android / iOS 设备通过 USB 使用电脑网络。电脑端提供 `revtether` 命令行和 macOS 菜单栏 App，设备端通过系统 VPN 转发 IPv4 流量。

支持 TCP、UDP、DNS 转发，以及本地 ICMP echo 响应；局域网地址保持直连。实现说明见下文，原始方案见 [design.md](design.md)，本轮代码审查见 [docs/review-2026-09-11.md](docs/review-2026-09-11.md)。

## 电脑端

需要 Go 1.27.1；可通过 [mise](https://mise.jdx.dev/) 安装仓库锁定的版本。

```bash
mise install
mise exec -- go build -o bin/revtether ./cmd/revtether
./bin/revtether devices
./bin/revtether run
```

常用参数：`revtether run --device <id-or-serial> --verbose --pcap capture.pcap`。其中 `--pcap` 会记录经过隧道的 IP 数据，请自行保管抓包文件。

Linux 上 iOS 设备需要系统 `usbmuxd`；未配对时先 `idevicepair pair`。Android 需要 PATH 里有 `adb`。

同一用户的 CLI 和菜单栏 App 共用一个转发进程，后启动的实例同步连接状态；菜单栏可暂停或恢复每台设备。只有持有转发连接的进程执行抓包，其他实例的 `--pcap` 会被忽略。ADB server 或 usbmuxd 重启后，请重启电脑端以重新订阅设备事件。

### macOS 菜单栏 App

需要 macOS 12 或更新版本及 Xcode Command Line Tools。打开即开始转发，点击菜单里的「退出」停止当前实例。

```bash
make app
open "dist/Reverse Tether.app"
```

可把 `Reverse Tether.app` 拖到 `/Applications`。日志在 `~/Library/Logs/ReverseTether.log`。从 Finder 启动时会自动把 Homebrew 和 Android `platform-tools` 加进 `PATH`，方便找到 `adb`。

`make app` 生成本机 ad-hoc 签名的 App；目前没有提供 Developer ID 签名、公证或 GitHub Release 安装包。

## iOS

需要 iOS 15 或更新版本。打开 `ios/ReverseTether.xcodeproj`（Bundle ID：`dev.fun.revtether` / `dev.fun.revtether.tunnel`），为 App 和 Tunnel 两个 target 选择自己的 Development Team，并配置 Network Extension 与 App Groups 签名，装到真机。首次会弹出添加 VPN 配置。

容器 App 在 `0.0.0.0:31416` 接收 usbmux 的本机代理连接，并检查对端为本机地址；通过 `sendProviderMessage` 与 VPN extension 交换 IP 包。需要保留可用的 Wi-Fi 或蜂窝接口，以满足当前 iOS 启动 VPN 的检查。后台转发依赖容器 App 保持运行，当前使用音频后台模式。

## Android

需要 Android 5.0 / API 21 或更新版本，电脑端安装 `adb`，设备开启 USB 调试并授权电脑。用 Android Studio 打开 `android/`，或使用 JDK 17 和 Android SDK 34 构建：

```bash
cd android
./gradlew assembleDebug
adb install -r app/build/outputs/apk/debug/app-debug.apk
```

打开设备 App 并授予 VPN 权限；Android 在 `127.0.0.1:31416` 等待电脑连接。「排除应用」中勾选的应用继续使用设备网络，修改后需停止并重新启动隧道才会生效。

`revtether install` 要等 APK 内嵌之后才能用；现在请直接用 Android Studio 安装。

## 开发检查

```bash
mise exec -- go test -race -timeout 45s ./...
mise exec -- go vet ./...
cd android && ./gradlew assembleDebug lintDebug
```

当前不支持 Windows、IPv6、上游代理链或内嵌 APK 安装。DNS 的 AAAA 查询返回空答案；ICMP echo 由电脑端本地回答，因此 ping 成功不能用于判断远端网络是否可达。
