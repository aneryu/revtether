# Reverse Tether

设备通过 USB 借用电脑网络。电脑端是单二进制 `revtether`，设备端打开 App 后监听 `127.0.0.1:31416`。

设计见 [design.md](design.md)。

## 电脑端

需要 [mise](https://mise.jdx.dev/)（本仓库已锁定 Go 1.27）。

```bash
mise exec -- go build -o bin/revtether ./cmd/revtether
./bin/revtether devices
./bin/revtether run
```

常用参数：`revtether run --device <id> --verbose --pcap capture.pcap`

Linux 上 iOS 设备需要系统 `usbmuxd`；未配对时先 `idevicepair pair`。Android 需要 PATH 里有 `adb`。

## iOS

打开 `ios/ReverseTether.xcodeproj`（Bundle ID：`dev.fun.revtether` / `dev.fun.revtether.tunnel`），选好 Development Team，装到真机。首次会弹出添加 VPN 配置。

App 启动即开隧道并在 `127.0.0.1:31416` 等待 `revtether run`。

## Android

用 Android Studio 打开 `android/`，装到真机后打开 App（会请求 VPN 权限）。「排除应用」里勾选的包不进隧道。

`revtether install` 要等 APK 内嵌之后才能用；现在请直接用 Android Studio 安装。

## 状态

M1 桌面 relay + iOS 工程、M2 Android 工程已按方案落地。第 15 节 M0 五项假设仍需真机确认（usbmux 能否打到 extension loopback、未配对 Connect、extension 内存、Android 14 FGS 类型、adb 双 OKAY）。
