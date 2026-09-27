BINARY  := qmx-hl2
MODULE  := github.com/rampa069/qmx-hl2
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X $(MODULE)/internal/config.Version=$(VERSION)
NPROC   := $(shell sysctl -n hw.ncpu 2>/dev/null || nproc)

# Native build. On macOS this needs `brew install portaudio pkg-config`; on Linux/Raspberry Pi
# OS, `apt install portaudio19-dev pkg-config`.
.PHONY: all build test vet tidy clean
all: build

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/qmx-hl2/

test:
	go test ./...

vet:
	go vet ./...

tidy:
	go mod tidy

clean:
	rm -f $(BINARY) $(BINARY)-*

# --- macOS universal targets -------------------------------------------------
.PHONY: darwin-arm64 darwin-amd64
darwin-arm64:
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=1 go build -ldflags "$(LDFLAGS)" -o $(BINARY)-darwin-arm64 ./cmd/qmx-hl2/
darwin-amd64:
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=1 go build -ldflags "$(LDFLAGS)" -o $(BINARY)-darwin-amd64 ./cmd/qmx-hl2/

# --- Static Linux cross-builds from macOS (musl) ----------------------------
# Ported from uSDX/audioStreamer. Unlike the original, PortAudio and ALSA are built per
# architecture, since the old arm64 target linked the amd64 libraries.
#
#   brew install FiloSottile/musl-cross/musl-cross cmake
#   (for arm64 also: brew reinstall musl-cross --with-aarch64)
#   make linux-amd64      # x86_64 Linux
#   make linux-arm64      # Raspberry Pi 4/5 with 64-bit OS
PA_VERSION   := 19.7.0
ALSA_VERSION := 1.2.12
DEPS         := $(CURDIR)/deps

.PHONY: linux-amd64 linux-arm64
linux-amd64:
	$(MAKE) linux-static GOARCH_T=amd64 TRIPLE=x86_64-linux-musl
linux-arm64:
	$(MAKE) linux-static GOARCH_T=arm64 TRIPLE=aarch64-linux-musl

.PHONY: linux-static
linux-static: $(DEPS)/$(TRIPLE)/pa/lib/libportaudio.a
	GOOS=linux GOARCH=$(GOARCH_T) CGO_ENABLED=1 CC=$(TRIPLE)-gcc \
		PKG_CONFIG_LIBDIR="$(DEPS)/$(TRIPLE)/pa/lib/pkgconfig" \
		go build -ldflags "$(LDFLAGS) -linkmode external -extldflags '-static'" \
		-o $(BINARY)-linux-$(GOARCH_T) ./cmd/qmx-hl2/

$(DEPS)/portaudio-$(PA_VERSION)/CMakeLists.txt:
	mkdir -p $(DEPS)
	curl -sL https://github.com/PortAudio/portaudio/archive/refs/tags/v$(PA_VERSION).tar.gz | tar xz -C $(DEPS)

$(DEPS)/alsa-lib-$(ALSA_VERSION)/configure:
	mkdir -p $(DEPS)
	curl -sL https://www.alsa-project.org/files/pub/lib/alsa-lib-$(ALSA_VERSION).tar.bz2 | tar xj -C $(DEPS)

# ALSA static library for one triple, built out of tree.
$(DEPS)/$(TRIPLE)/sysroot/usr/lib/libasound.a: $(DEPS)/alsa-lib-$(ALSA_VERSION)/configure
	mkdir -p $(DEPS)/$(TRIPLE)/alsa-build
	cd $(DEPS)/$(TRIPLE)/alsa-build && bash $(DEPS)/alsa-lib-$(ALSA_VERSION)/configure \
		--host=$(TRIPLE) --prefix=/usr --enable-static --disable-shared \
		--disable-python --disable-topology CC=$(TRIPLE)-gcc
	$(MAKE) -C $(DEPS)/$(TRIPLE)/alsa-build -j$(NPROC)
	$(MAKE) -C $(DEPS)/$(TRIPLE)/alsa-build install DESTDIR=$(DEPS)/$(TRIPLE)/sysroot

# PortAudio static library (ALSA only) for one triple, plus a pkg-config file for cgo.
$(DEPS)/$(TRIPLE)/pa/lib/libportaudio.a: $(DEPS)/portaudio-$(PA_VERSION)/CMakeLists.txt $(DEPS)/$(TRIPLE)/sysroot/usr/lib/libasound.a
	mkdir -p $(DEPS)/$(TRIPLE)/pa-build
	cd $(DEPS)/$(TRIPLE)/pa-build && cmake $(DEPS)/portaudio-$(PA_VERSION) \
		-DCMAKE_POLICY_VERSION_MINIMUM=3.5 \
		-DCMAKE_SYSTEM_NAME=Linux \
		-DCMAKE_C_COMPILER=$(TRIPLE)-gcc \
		-DCMAKE_C_FLAGS="-I$(DEPS)/$(TRIPLE)/sysroot/usr/include" \
		-DALSA_INCLUDE_DIR="$(DEPS)/$(TRIPLE)/sysroot/usr/include" \
		-DALSA_LIBRARY="$(DEPS)/$(TRIPLE)/sysroot/usr/lib/libasound.a" \
		-DPA_USE_ALSA=ON -DPA_USE_JACK=OFF \
		-DPA_BUILD_TESTS=OFF -DPA_BUILD_EXAMPLES=OFF
	$(MAKE) -C $(DEPS)/$(TRIPLE)/pa-build portaudio_static -j$(NPROC)
	mkdir -p $(DEPS)/$(TRIPLE)/pa/lib/pkgconfig $(DEPS)/$(TRIPLE)/pa/include
	cp $(DEPS)/$(TRIPLE)/pa-build/libportaudio.a $(DEPS)/$(TRIPLE)/pa/lib/
	cp $(DEPS)/portaudio-$(PA_VERSION)/include/portaudio.h $(DEPS)/$(TRIPLE)/pa/include/
	printf 'prefix=$(DEPS)/$(TRIPLE)/pa\nlibdir=$${prefix}/lib\nincludedir=$${prefix}/include\n\nName: PortAudio\nDescription: PortAudio (static, musl, ALSA)\nVersion: 19\n\nLibs: -L$${libdir} -lportaudio -L$(DEPS)/$(TRIPLE)/sysroot/usr/lib -lasound -lm -lpthread -ldl -lrt\nCflags: -I$${includedir}\n' \
		> $(DEPS)/$(TRIPLE)/pa/lib/pkgconfig/portaudio-2.0.pc
