# Reverse Tether

Use your computer's internet connection on Android and iOS devices over USB. The computer runs the `revtether` CLI or the macOS menu bar app, while the device forwards IPv4 traffic through a system VPN.

Supports TCP, UDP, and DNS forwarding, with local ICMP echo replies. LAN addresses remain directly accessible. See below for the current implementation, [design.md](design.md) for the original design, and [docs/review-2026-09-11.md](docs/review-2026-09-11.md) for the code review.

## Desktop

Requires Go 1.27.1. Use [mise](https://mise.jdx.dev/) to install the version pinned by this repository.

```bash
mise install
mise exec -- go build -o bin/revtether ./cmd/revtether
./bin/revtether devices
./bin/revtether run
```

Common options: `revtether run --device <id-or-serial> --verbose --pcap capture.pcap`. The `--pcap` option records IP traffic passing through the tunnel; keep capture files secure.

iOS devices on Linux require the system `usbmuxd` service. Run `idevicepair pair` first if the device is not paired. Android requires `adb` in `PATH`.

The CLI and menu bar app share one forwarding process per user. Instances started later mirror its connection status, and the menu bar can pause or resume forwarding for each device. Only the process that owns the relay captures packets; other instances ignore `--pcap`. After restarting the ADB server or usbmuxd, restart the desktop app or CLI to subscribe to device events again.

### macOS menu bar app

Requires macOS 12 or later and Xcode Command Line Tools. Opening the app starts forwarding. Use the Quit menu item to stop the current instance.

```bash
make app
open "dist/Reverse Tether.app"
```

You can drag `Reverse Tether.app` into `/Applications`. Logs are written to `~/Library/Logs/ReverseTether.log`. When launched from Finder, the app adds Homebrew and Android `platform-tools` directories to `PATH` so it can find `adb`.

`make app` produces an app with a local ad-hoc signature. Developer ID signing, notarization, and GitHub Release installers are not currently provided.

## iOS

Requires iOS 15 or later. Open `ios/ReverseTether.xcodeproj` (bundle IDs: `dev.fun.revtether` / `dev.fun.revtether.tunnel`), select your Development Team for both the App and Tunnel targets, configure signing for Network Extension and App Groups, and install on a physical device. The first launch prompts you to add a VPN configuration.

The container app accepts connections from the device's local usbmux proxy on `0.0.0.0:31416` and verifies that the peer address belongs to the device. It exchanges IP packets with the VPN extension through `sendProviderMessage`. Keep a usable Wi-Fi or cellular interface enabled to satisfy the current iOS VPN startup check. Background forwarding depends on the container app staying active; the current implementation uses audio background mode.

## Android

Requires Android 5.0 / API 21 or later. Install `adb` on the computer, enable USB debugging on the device, and authorize the computer. Open `android/` in Android Studio, or build with JDK 17 and Android SDK 34:

```bash
cd android
./gradlew assembleDebug
adb install -r app/build/outputs/apk/debug/app-debug.apk
```

Open the device app and grant VPN permission. Android listens on `127.0.0.1:31416` for the computer. Apps selected in the exclusion list continue using the device's network. Stop and restart the tunnel for changes to take effect.

`revtether install` will become available once the APK is embedded. For now, install through Android Studio or the `adb install` command above.

## Development checks

```bash
mise exec -- go test -race -timeout 45s ./...
mise exec -- go vet ./...
cd android && ./gradlew assembleDebug lintDebug
```

Windows, IPv6, upstream proxy chains, and installation from an embedded APK are not currently supported. DNS AAAA queries return empty answers. ICMP echo requests are answered locally by the computer, so a successful ping does not establish that a remote destination is reachable.
