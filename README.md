# qmx-hl2

**Use a QRP Labs QMX / QMX+ as a Hermes-Lite 2 from any openHPSDR client.**

`qmx-hl2` is a small daemon that sits between a QMX and your network. To SDR programs such as
Zeus, Thetis, piHPSDR or SparkSDR it looks like a Hermes-Lite 2 speaking openHPSDR Protocol 1.
Behind the scenes it streams the QMX's I/Q receive audio as HPSDR receive data and turns the
client's transmit I/Q into something the QMX can transmit.

> Unofficial project. Not affiliated with or endorsed by QRP Labs or the Hermes-Lite project.
> "QMX" and "Hermes-Lite" are the names of their respective owners' products, used here only
> to describe compatibility.

## Status

Working on the air with **[Zeus](https://github.com/abhishekprakash22/zeus)** (September 2026), using its
built-in FT8/FT4/WSPR and SSTV modes as well as external WSJT-X:

| Function | Status |
|---|---|
| Discovery as a Hermes-Lite 2 | works |
| RX spectrum/waterfall at 48/96/192/384 kHz | works (the QMX itself covers 48 kHz) |
| FT8 / FT4 / WSPR / JS8 transmit | works: complete FT8 QSOs, spots on PSK Reporter |
| CW transmit | works (client-generated CW keyed through the QMX) |
| SSB voice transmit | works |
| SSTV transmit | works: pictures received straight by remote stations |
| FreeDV RADE V1 transmit | works: first QSO on 28 Sep 2026 with EC7C on 40 m, good reports (Zeus's built-in RADE); the first FreeDV QSO for both stations |
| Power / SWR meters in the client | works |
| Other clients (Thetis, piHPSDR, SparkSDR, Quisk) | not yet tested |
| Linux x86-64 | tested (Debian 13) |
| macOS | signed and notarized binaries; used for development |
| Raspberry Pi (Linux arm64) | builds; not yet tested on hardware |
| Windows x86-64 | tested |

## How it works

```
 openHPSDR client  <--UDP 1024, Protocol 1-->  qmx-hl2  <--USB audio (I/Q in, audio out)-->  QMX
 (Zeus, Thetis...)                                       <--USB serial (CAT)--------------->
```

**Receive.** The QMX's *IQ mode* (`Q91;`) streams its raw 48 kHz, 24-bit I/Q over the USB sound
card. `qmx-hl2` keeps the QMX tuned so that receiver 1 is in the middle of that 48 kHz window.
It removes DC, corrects I/Q gain and phase mismatch, upsamples to the rate the client asks
for, shifts each receiver (up to 12) to its own frequency, and sends it all as HPSDR EP6 packets.
At 96 kHz and above, the part of the display the QMX cannot cover is filled with low-level
noise.

**Transmit** (only with `-tx`). The client's transmit I/Q is inspected at the start of each
transmission:

- **Single tone** (FT8, FT4, WSPR, JS8, CW, TUNE): the daemon tracks the instantaneous frequency
  and sends it to the QMX with the CAT tone command (`TA`). The QMX stays in Digi mode and
  generates a clean, keyed carrier at exactly the right frequency.
- **Anything else** (voice, SSTV, FreeDV, PSK, RTTY, two-tone): the QMX is switched to USB or LSB with USB
  audio as the source. During the over the client's TX pacing is locked to the QMX's playback
  clock, so its audio goes through a small buffer at a constant delay, without resampling
  (SSTV pictures stay straight).
- **AM**: the QMX cannot transmit AM. An AM over starts out as a plain carrier (like a TUNE);
  once the modulation shows, it is moved to USB, so the voice goes out without the carrier.
  Likewise an over that starts as a steady tone but then shifts by more than 75 Hz (RTTY's
  mark/space, MFSK) moves to SSB, since `TA` cannot follow its short bits cleanly.

The receive and transmit sideband conventions follow what HPSDR clients expect from real
hardware, and the RX dial and Digi mode are restored after every transmission.

## Requirements

- **QMX or QMX+** with firmware **1_02_004 or later** (CAT `TA`; SSB needs 1_02_000 or
  later). Tested with **1_03_002**. The band must be one your QMX can transmit on.
- A computer with a USB port: Linux (x86-64 or arm64), macOS, or Windows.
- An openHPSDR Protocol 1 client that supports the Hermes-Lite 2.

## Building

Needs Go 1.25 or later, PortAudio and pkg-config.

```sh
# macOS
brew install portaudio pkg-config
# Debian / Ubuntu / Raspberry Pi OS
sudo apt install portaudio19-dev pkg-config

make build        # ./qmx-hl2 for this machine
make test
```

Self-contained binaries for every platform can be built on macOS. They need `cmake`,
`musl-cross` (Linux) and `mingw-w64` (Windows). PortAudio (and ALSA for Linux) are downloaded
and linked statically, so the binaries need nothing installed on the target machine.

```sh
make linux-amd64      # qmx-hl2-linux-amd64 (static)
make linux-arm64      # qmx-hl2-linux-arm64 (static, Raspberry Pi 64-bit)
make darwin-arm64     # qmx-hl2-darwin-arm64 (Apple silicon)
make darwin-amd64     # qmx-hl2-darwin-amd64 (Intel Mac)
make windows-amd64    # qmx-hl2-windows-amd64.exe
make release-binaries # all of the above
```

## Quick start

1. Connect the QMX by USB. Leave VOX off. Don't make the QMX your computer's default sound
   output: while any other program plays audio to it, the QMX ignores the tone command used
   for FT8/CW.
2. Check that it is found:
   ```sh
   ./qmx-hl2 -list
   ```
3. Receive only:
   ```sh
   ./qmx-hl2 -v
   ```
   With transmit enabled:
   ```sh
   ./qmx-hl2 -v -tx
   ```
4. In your client, discover radios. A **Hermes-Lite 2** should appear. Start it.
5. Operate as you would with a real HL2: the client's own digital modes (Zeus has FT8, FT4,
   WSPR and SSTV built in) or external programs such as WSJT-X all work. The QMX follows the
   client's TX frequency and mode.

On exit (Ctrl-C) the daemon puts the QMX back to the frequency, mode, IQ-mode and CAT-watchdog
settings it found.

## Options

| Option | Default | Meaning |
|---|---|---|
| `-tx` | off | Allow transmitting when the client keys MOX |
| `-txmode` | `auto` | `auto` (tone for FT8/CW, SSB for everything else), `tone`, or `ssb` |
| `-ssbgain` | 0 | dB of gain on SSB transmit audio |
| `-maxtx` | 3m | Longest continuous transmission (raise for long SSTV modes) |
| `-txwav` | | Save each SSB over, exactly as played to the QMX, as a WAV file in this directory (decode SSTV offline) |
| `-parrot` | | No QMX: a virtual radio replays each transmission to the client this long after it starts (`15s` for FT8, `7.5s` for FT4, so the replay lands in your receive slot). Tests the client and the whole transmit chain without RF; implies `-tx` |
| `-audio` | `QMX` | Sound device name or substring (see `-list`) |
| `-serial` | auto | QMX serial port (auto-detected by USB ID) |
| `-listen` | `:1024` | UDP address to listen on |
| `-mac` | `00:1c:c0:a2:51:4d` | MAC address reported to clients |
| `-watchdog` | 12s | Stop streaming if the client goes silent |
| `-rxgain` | 0 | dB of digital gain on receive I/Q |
| `-iqbal` | on | Correct QMX I/Q gain/phase mismatch |
| `-noisefill` | on | Fill the band outside the QMX's 48 kHz with low-level noise |
| `-looffset` | 0 | Hz between RX1 and the QMX's I/Q centre (e.g. `-4000` moves its low-frequency noise hump off the display centre) |
| `-swapiq` | off | Invert the RX spectrum convention (if a client shows it mirrored) |
| `-txswapiq` | off | Invert the TX I/Q convention (if a client's tones come out mirrored) |
| `-v` | off | Verbose console logging (a log file is always written to `-logdir`) |
| `-logdir` | `logs` | Directory for the timestamped log files |
| `-logfile` | | Log to this file instead of a timestamped one in `-logdir` |
| `-rate` | 48000 | QMX audio sample rate (fixed at 48000 by the hardware) |
| `-frames` | 240 | Frames per audio buffer (240 = 5 ms) |
| `-list` | | List sound devices and serial ports, then exit |
| `-probe 60s [-iq] [-playback]` | | Bench check: I/Q levels and QMX clock rates |
| `-version` | | Print the version and exit |

## Transmit safety

- Nothing is ever keyed without `-tx`. The CAT layer itself refuses transmit commands
  unless transmit was enabled.
- The QMX's own CAT watchdog is set to 3 s while the daemon runs, so the radio drops to receive
  if the daemon dies mid-transmission. The original setting is restored on exit.
- The QMX is also unkeyed:
  - when the client stops sending transmit packets for 150 ms;
  - after `-maxtx`;
  - when the SWR stays above 3:1 (after a short grace period for the key-down transient);
  - when a tone falls outside what the QMX accepts;
  - when the QMX's own SWR protection has locked transmit (CAT `SR`, firmware 1_04_004 or
    later). The log says so and how to clear it. That protection samples every millisecond,
    and on firmware 1_04_010 to 1_04_015 the key-down transient alone tripped it on a good
    antenna with its threshold at 3 and 5, but not at 7 or 9 (QMX+). If your antenna is fine
    and it keeps tripping, raise its threshold to about 7.

  After any of these, TX stays off until the client releases MOX. A tone over that reads 0 W
  also logs a warning, since the QMX may have locked transmit for another reason.

## Limitations and notes

- **Bandwidth:** the QMX delivers 48 kHz of spectrum. At higher client rates the rest of the
  display is filled with noise, not signals.
- **Receivers:** extra receivers only show signals inside that 48 kHz window.
- **Mode switching:** the QMX display stays in DIGI for FT8 and CW (they are sent with the tone
  command) and shows USB/LSB during voice transmissions.
- **Decision delay:** each transmission takes 20–150 ms to be classified. Voice is buffered, so
  it is only delayed. In `auto` mode the first CW element can be shortened a little; use
  `-txmode tone` if you only do CW and digital modes.
- **No PureSignal**, no wideband (EP4) data.
- **Retuning:** the QMX is retuned as you move receiver 1, so a client dragging its VFO causes
  frequent QMX retunes.
- **Firmware quirks found along the way:**
  - `TA0` returns the QMX to receive, so the daemon re-keys for every CW element.
  - The QMX ignores `TA` while USB audio is streaming to it.
  - With USB audio playing, the QMX's I/Q capture runs about 1.1% fast. The daemon paces the
    network stream from its own clock while transmitting to compensate.

## Documentation

Design notes, protocol references and bench measurements are in [`doc/`](doc/):

- [`doc/README.md`](doc/README.md): index and feasibility summary
- [`doc/hl2/protocol1.md`](doc/hl2/protocol1.md): the Protocol 1 / HL2 wire format as implemented
- [`doc/qmx/`](doc/qmx/): QMX IQ mode, CAT commands, transmit paths
- [`doc/design-considerations.md`](doc/design-considerations.md), [`doc/prior-art.md`](doc/prior-art.md)

## Acknowledgements

Protocol behaviour was checked against the Hermes-Lite 2 gateware and wiki, piHPSDR's
`hpsdrsim`, Zeus, and N1GP's `rtl_hpsdr`. Hans Summers' QMX manuals made the CAT and IQ-mode
details possible.

## License

Copyright (C) 2026 Ramon Martinez.

qmx-hl2 is free software: you can redistribute it and/or modify it under the terms of the GNU
General Public License as published by the Free Software Foundation, either version 2 of the
License, or (at your option) any later version. See [`LICENSE`](LICENSE).

Third-party components:

- [PortAudio](https://www.portaudio.com/): MIT-style license.
- [gordonklaus/portaudio](https://github.com/gordonklaus/portaudio) Go bindings: MIT.
- [go.bug.st/serial](https://github.com/bugst/go-serial): BSD-3-Clause.
- The static Linux binaries also include [alsa-lib](https://github.com/alsa-project/alsa-lib),
  LGPL-2.1-or-later. The exact source used is the upstream `alsa-lib-1.2.12` release, which the
  `Makefile` downloads.

The reference documents behind `doc/` belong to their authors (QRP Labs, the Hermes-Lite
project, TAPR/openHPSDR) and are linked, not copied, from each `doc/*/ref/README.md`.
