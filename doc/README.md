# QMX → Hermes Lite 2 emulator — documentation

Goal: a daemon that presents a QRP Labs QMX/QMX+ as a Hermes Lite 2 (openHPSDR Protocol 1, UDP 1024)
so HPSDR clients (Thetis, SparkSDR, piHPSDR/deskHPSDR, Quisk, linHPSDR…) can use it.

## Index

| File | Contents |
|---|---|
| `hl2/protocol1.md` | Wire spec: discovery, start/stop, EP2/EP6, C&C register map, TX timing, watchdog (cites gateware lines) |
| `hl2/client-behavior.md` | How the clients discover, drive and pace an HL2, plus the minimum an emulator must implement |
| `hl2/ref/` | Original specs (USB protocol V1.60, Metis, HL2 wiki pages) |
| `qmx/qmx-overview.md` | Hardware, bands, USB composite device, firmware versions |
| `qmx/iq-mode.md` | IQ mode (`Q91;`), audio format, LO offset |
| `qmx/tx-audio-and-ssb.md` | DIGI TX (tone following), `TA` command, SSB (1_02_000+), CW |
| `qmx/cat-commands.md` | Full CAT table (rev 1_04_004) |
| `qmx/ref/` | CAT and operating PDFs, schematics, release notes |
| `prior-art.md` | Existing HPSDR emulators and what we can reuse |
| `design-considerations.md` | TX IQ→audio, clock drift, language choice, RX bandwidth |

## Feasibility summary

**RX — feasible.**
- The QMX sends IQ over USB audio: 48 kHz, 24-bit stereo, L=I, R=Q (`Q91;`).
- The LO sits 12 kHz below the dial. To put frequency F at the centre, send `FA` = F + 12000.
- Limits:
  - only 48 kHz of span, so the HL2 rate is always 48k (higher rates would need upsampling with an empty spectrum);
  - there is one real receiver. Clients ask for up to 4 (Thetis always does). The extra receivers either copy RX1, or are shifted in software when they fall inside the 48 kHz window.
  - no IQ correction in firmware, so the daemon has to correct the image itself;
  - `Q9` is volatile, so it must be re-sent and checked.

**TX — feasible with limits.** The HL2 protocol carries no mode, only TX IQ + MOX. The daemon has to translate it:
- *Digital / CW*:
  - measure the frequency of the TX IQ;
  - play a full-scale tone (above 80% FS) in DIGI mode, or set it with `TA<Hz>;`;
  - covers FT8/FT4/WSPR/RTTY-FSK and CW with MOX.
- *SSB voice* (firmware ≥1_02_000): take the real part of the IQ as audio → QMX in SSB mode with USB input (`SS0;`). Bandwidth up to 3.2 kHz, IMD about −40 dB.
- No RX during TX: the daemon blanks the IQ and keeps producing EP6 packets on a timer, which is what clients pace themselves on.
- There is no CAT command for power. The HL2 drive level can be mapped to audio amplitude (SSB) or ignored.

**Timing — the critical point.**
- The EP6 stream is the clients' master clock.
- It is generated from the QMX capture clock.
- It needs a TX jitter buffer (20–40 ms), a TX safety timeout, and ACK replies (Quisk requires them).

**Must be checked on the real radio before we trust the design:**
1. whether USB-audio TX (DIGI and SSB) works while IQ mode is on;
2. the actual image rejection;
3. the sign of the IQ offset and the I/Q order;
4. whether the QMX echoes set commands;
5. how SSB is selected over CAT.

**Prior art:**
- No existing sound-card-radio → HPSDR bridge was found.
- Main references:
  - `pihpsdr/src/hpsdrsim.c` (full P1 simulator with HL2 specifics; also a test peer);
  - Red Pitaya `sdr-transceiver-hpsdr` (MIT);
  - `rtl_hpsdr`;
  - `uSDX/audioStreamer` (Go audio + CAT daemon, already ours).
