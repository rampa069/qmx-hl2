BINARY  := qmx-hl2
MODULE  := github.com/rampa069/qmx-hl2
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X $(MODULE)/internal/config.Version=$(VERSION)
NPROC   := $(shell sysctl -n hw.ncpu 2>/dev/null || nproc)
PA_VERSION   := 19.7.0
ALSA_VERSION := 1.2.12
DEPS         := $(CURDIR)/deps

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

# --- macOS: PortAudio built statically per architecture ---------------------
# The resulting binaries only depend on system frameworks (no Homebrew needed to run).
.PHONY: darwin-arm64 darwin-amd64 darwin-static
darwin-arm64:
	$(MAKE) darwin-static GOARCH_T=arm64 MACARCH=arm64
darwin-amd64:
	$(MAKE) darwin-static GOARCH_T=amd64 MACARCH=x86_64

darwin-static: $(DEPS)/darwin-$(MACARCH)/pa/lib/libportaudio.a
	GOOS=darwin GOARCH=$(GOARCH_T) CGO_ENABLED=1 CGO_CFLAGS="-arch $(MACARCH)" CGO_LDFLAGS="-arch $(MACARCH)" \
		PKG_CONFIG_LIBDIR="$(DEPS)/darwin-$(MACARCH)/pa/lib/pkgconfig" \
		go build -ldflags "$(LDFLAGS)" -o $(BINARY)-darwin-$(GOARCH_T) ./cmd/qmx-hl2/

$(DEPS)/darwin-$(MACARCH)/pa/lib/libportaudio.a: $(DEPS)/portaudio-$(PA_VERSION)/CMakeLists.txt
	mkdir -p $(DEPS)/darwin-$(MACARCH)/pa-build
	cd $(DEPS)/darwin-$(MACARCH)/pa-build && cmake $(DEPS)/portaudio-$(PA_VERSION) \
		-DCMAKE_POLICY_VERSION_MINIMUM=3.5 -DCMAKE_OSX_ARCHITECTURES=$(MACARCH) \
		-DCMAKE_OSX_DEPLOYMENT_TARGET=11.0 -DPA_BUILD_TESTS=OFF -DPA_BUILD_EXAMPLES=OFF
	$(MAKE) -C $(DEPS)/darwin-$(MACARCH)/pa-build portaudio_static -j$(NPROC)
	mkdir -p $(DEPS)/darwin-$(MACARCH)/pa/lib/pkgconfig $(DEPS)/darwin-$(MACARCH)/pa/include
	cp $(DEPS)/darwin-$(MACARCH)/pa-build/libportaudio.a $(DEPS)/darwin-$(MACARCH)/pa/lib/
	cp $(DEPS)/portaudio-$(PA_VERSION)/include/portaudio.h $(DEPS)/darwin-$(MACARCH)/pa/include/
	printf 'prefix=$(DEPS)/darwin-$(MACARCH)/pa\nlibdir=$${prefix}/lib\nincludedir=$${prefix}/include\n\nName: PortAudio\nDescription: PortAudio (static, CoreAudio)\nVersion: 19\n\nLibs: -L$${libdir} -lportaudio -framework CoreAudio -framework AudioToolbox -framework AudioUnit -framework CoreFoundation -framework CoreServices\nCflags: -I$${includedir}\n' \
		> $(DEPS)/darwin-$(MACARCH)/pa/lib/pkgconfig/portaudio-2.0.pc

# --- Windows x86-64 from macOS (mingw-w64), static ---------------------------
#   brew install mingw-w64 cmake
.PHONY: windows-amd64
windows-amd64: $(DEPS)/win64/pa/lib/libportaudio.a
	GOOS=windows GOARCH=amd64 CGO_ENABLED=1 CC=x86_64-w64-mingw32-gcc \
		PKG_CONFIG_LIBDIR="$(DEPS)/win64/pa/lib/pkgconfig" \
		go build -ldflags "$(LDFLAGS) -linkmode external -extldflags '-static'" \
		-o $(BINARY)-windows-amd64.exe ./cmd/qmx-hl2/

$(DEPS)/win64/pa/lib/libportaudio.a: $(DEPS)/portaudio-$(PA_VERSION)/CMakeLists.txt
	mkdir -p $(DEPS)/win64/pa-build
	cd $(DEPS)/win64/pa-build && cmake $(DEPS)/portaudio-$(PA_VERSION) \
		-DCMAKE_POLICY_VERSION_MINIMUM=3.5 -DCMAKE_SYSTEM_NAME=Windows \
		-DCMAKE_C_COMPILER=x86_64-w64-mingw32-gcc -DCMAKE_CXX_COMPILER=x86_64-w64-mingw32-g++ \
		-DCMAKE_RC_COMPILER=x86_64-w64-mingw32-windres \
		-DPA_USE_WASAPI=ON -DPA_USE_WMME=ON -DPA_USE_WDMKS=OFF -DPA_USE_DS=OFF -DPA_USE_ASIO=OFF \
		-DPA_BUILD_TESTS=OFF -DPA_BUILD_EXAMPLES=OFF
	$(MAKE) -C $(DEPS)/win64/pa-build portaudio_static -j$(NPROC)
	mkdir -p $(DEPS)/win64/pa/lib/pkgconfig $(DEPS)/win64/pa/include
	cp $(DEPS)/win64/pa-build/libportaudio.a $(DEPS)/win64/pa/lib/
	cp $(DEPS)/portaudio-$(PA_VERSION)/include/portaudio.h $(DEPS)/win64/pa/include/
	printf 'prefix=$(DEPS)/win64/pa\nlibdir=$${prefix}/lib\nincludedir=$${prefix}/include\n\nName: PortAudio\nDescription: PortAudio (static, WASAPI+WMME)\nVersion: 19\n\nLibs: -L$${libdir} -lportaudio -lwinmm -lole32 -luuid -lsetupapi\nCflags: -I$${includedir}\n' \
		> $(DEPS)/win64/pa/lib/pkgconfig/portaudio-2.0.pc

# --- Everything, for a release ------------------------------------------------
.PHONY: release-binaries
release-binaries: linux-amd64 linux-arm64 darwin-arm64 darwin-amd64 windows-amd64

# --- Static Linux cross-builds from macOS (musl) ----------------------------
# Ported from uSDX/audioStreamer. Unlike the original, PortAudio and ALSA are built per
# architecture, since the old arm64 target linked the amd64 libraries.
#
#   brew install FiloSottile/musl-cross/musl-cross cmake
#   (for arm64 also: brew reinstall musl-cross --with-aarch64)
#   make linux-amd64      # x86_64 Linux
#   make linux-arm64      # Raspberry Pi 4/5 with 64-bit OS

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
