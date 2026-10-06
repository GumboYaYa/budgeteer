.PHONY: build test vet run generate css tools

build:
	go build -o budgeteer ./cmd/budgeteer

test:
	go test ./...

vet:
	go vet ./...

# make run ARGS="account list"
run:
	go run ./cmd/budgeteer $(ARGS)

# Regenerate after editing *.templ or assets/app.css. The results are
# committed, so a plain `make build` needs neither tool.
generate:
	go tool templ generate

css: bin/tailwindcss bin/daisyui.mjs
	bin/tailwindcss -i internal/web/assets/app.css -o internal/web/static/app.css --minify

tools: bin/tailwindcss bin/daisyui.mjs

TAILWIND_OS := $(shell uname -s | sed 's/Darwin/macos/;s/Linux/linux/')
TAILWIND_ARCH := $(shell uname -m | sed 's/x86_64/x64/;s/aarch64/arm64/')

bin/tailwindcss:
	mkdir -p bin
	curl -fsSL -o $@ https://github.com/tailwindlabs/tailwindcss/releases/latest/download/tailwindcss-$(TAILWIND_OS)-$(TAILWIND_ARCH)
	chmod +x $@

bin/daisyui.mjs:
	mkdir -p bin
	curl -fsSL -o $@ https://github.com/saadeghi/daisyui/releases/latest/download/daisyui.mjs
