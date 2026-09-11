.PHONY: revtether test app android

revtether:
	mise exec -- go build -o bin/revtether ./cmd/revtether

test:
	mise exec -- go test ./...

APP_BUNDLE = dist/Reverse Tether.app
APP_BIN = $(APP_BUNDLE)/Contents/MacOS/ReverseTether
APP_RES = $(APP_BUNDLE)/Contents/Resources
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

app:
	@if [ "$$(uname -s)" != Darwin ]; then echo "macOS only" >&2; exit 1; fi
	mkdir -p "$(APP_BUNDLE)/Contents/MacOS" "$(APP_RES)"
	cp macos/Info.plist "$(APP_BUNDLE)/Contents/Info.plist"
	@if [ ! -f macos/AppIcon.icns ]; then bash macos/make-icns.sh macos/AppIcon.icns; fi
	@if [ -f macos/AppIcon.icns ]; then cp macos/AppIcon.icns "$(APP_RES)/AppIcon.icns"; fi
	CGO_ENABLED=1 mise exec -- go build -ldflags "-X main.version=$(VERSION)" -o "$(APP_BIN)" ./cmd/revtether-app
	codesign --force --deep --sign - "$(APP_BUNDLE)"
	@echo "built $(APP_BUNDLE)"

android:
	cd android && ./gradlew assembleDebug
	adb install -r android/app/build/outputs/apk/debug/app-debug.apk
