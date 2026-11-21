PREFIX ?= $(HOME)/.local
LDFLAGS := -s -w

.PHONY: build install test clean

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o ted .

install: build
	install -d $(PREFIX)/bin
	install -m 755 ted $(PREFIX)/bin/ted

test:
	go test ./...

clean:
	rm -f ted
