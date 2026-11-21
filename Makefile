PREFIX  ?= $(HOME)/.local
VERSION := $(shell sed -n 's/^var version = "\(.*\)"/\1/p' main.go)
LDFLAGS := -s -w
UNAME   := $(shell uname -s)
APPDIR  := $(HOME)/Applications

.PHONY: build app install test clean

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o ted .

# macOS app bundle, so ted shows up in Spotlight, Launchpad, Raycast and the Dock
app: build
	rm -rf build/ted.app build/ted.iconset
	mkdir -p build/ted.app/Contents/MacOS build/ted.app/Contents/Resources build/ted.iconset
	cp ted build/ted.app/Contents/MacOS/ted
	sed "s/@VERSION@/$(VERSION)/" packaging/Info.plist > build/ted.app/Contents/Info.plist
	go run ./tools/mkicon -out build/ted.iconset -sizes 16,32,64,128,256,512,1024 -name "raw_%d.png"
	cd build/ted.iconset && for s in 16 32 128 256 512; do \
		cp raw_$$s.png icon_$${s}x$${s}.png; \
		cp raw_$$((s*2)).png icon_$${s}x$${s}@2x.png; \
	done && rm raw_*.png
	iconutil -c icns build/ted.iconset -o build/ted.app/Contents/Resources/ted.icns
	rm -rf build/ted.iconset

install: build
	install -d $(PREFIX)/bin
	install -m 755 ted $(PREFIX)/bin/ted
ifeq ($(UNAME),Darwin)
	$(MAKE) app
	@test -n "$(HOME)" || (echo "HOME is not set" && exit 1)
	mkdir -p "$(APPDIR)"
	if [ -d "$(APPDIR)/ted.app" ]; then rm -r "$(APPDIR)/ted.app"; fi
	cp -R build/ted.app "$(APPDIR)/ted.app"
	@echo "installed $(APPDIR)/ted.app"
endif
ifeq ($(UNAME),Linux)
	# desktop entry and icon, so launchers (Omarchy's Walker, GNOME, ...) list ted
	install -d $(HOME)/.local/share/applications $(HOME)/.local/share/icons/hicolor/256x256/apps
	sed "s|@BIN@|$(PREFIX)/bin/ted|" packaging/ted.desktop > $(HOME)/.local/share/applications/ted.desktop
	go run ./tools/mkicon -out $(HOME)/.local/share/icons/hicolor/256x256/apps -sizes 256 -name ted.png
	-update-desktop-database $(HOME)/.local/share/applications 2>/dev/null
endif

test:
	go test ./...

clean:
	rm -rf ted build
