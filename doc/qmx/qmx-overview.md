# QMX / QMX+ overview (hardware, USB, firmware)

Research date: 2026-09-27. Firmware current at that date: **1_04_015 (beta, 24-Sep-2026)**;
latest "Stable" tagged release **1_04_010 (14-Sep-2026)**. Current manuals: operating manual
and CAT manual rev **1_04_004** (23-Jul-2026). Local copies are in `ref/` (see `ref/README.md`).

Legend: **[doc]** = stated in QRP Labs documentation; **[field]** = observed by third-party
software authors / forum users; **[UNVERIFIED]** = inference, needs bench confirmation.

## 1. Hardware relevant to an HL2 emulator

| Item | QMX | QMX+ | Source |
|---|---|---|---|
| Bands | 3 kit versions: 80/60/40/30/20 m; 60/40/30/20/17/15 m; 20/17/15/12/11/10 m (5-6 bands). "Band version" chosen at first power-up, read-only afterwards | 160-6 m, full coverage (12 bands incl. 11 m) | qmx.html, qmxp.html, op manual s. 5 "Band version" |
| Band table | 16 columns in "Band config." (name, RF gain, freq min/center/max, sweep, BPF 0-3, LPF 0-2, PIN bias) | same | op manual p.103 |
| RX outside band | Restricted to configured bands unless System config "RX outside band" = ON (since 1_00_022) | same | fw log 1_00_022 |
| TX power | 3-5 W at 12 V (4-5 W if built for 9 V). Power set indirectly via **Protection / Max. PA voltage** (square law, no direct watts setting) | same | qmx.html, op manual p.63 |
| MCU | STM32F446, 168 MHz Cortex-M4F | same | op manual p.3 |
| RX front end | SWR bridge -> PIN-diode switched LPF (kept in circuit on RX) -> T/R switch Q508 -> switched BPF (4) -> double-balanced **Quadrature Sampling Detector** -> LM4562 differential instrumentation amps -> **PCM1804 24-bit stereo ADC @ 48 ksps** over I2S | same | op manual p.86 |
| Synth | Si5351A or MS5351M, 25 MHz TCXO (±5 Hz typ). Clk2 = TX / signal generator | same | op manual |
| Audio out | CS4334 stereo DAC -> earphone jack | same | op manual p.86 |
| Internal DSP | superhet with **12 kHz IF**: mix to 12 kHz, decimate /4 to 12 ksps, Hilbert, SSB/CW filters, back to 48 ksps | same | op manual p.86 |
| Image rejection | No I/Q amplitude/phase correction in firmware ("no attempt is made to compensate"); typically adequate (example 75 dB, varies "wildly" unit to unit and band to band). Product page: 60-70 dB unwanted sideband rejection. 160 m quadrature fix in 1_04_007 | same | op manual p.111-112, fw 1_04_007 |
| T/R | solid state, full QSK capable, RX latency ~15 ms on CW | same | qmxp.html |
| Full duplex | **No.** Single QSD shared, T/R switch opens RX path on TX. IQ stream continues during TX but carries garbage/leakage [field, OK1IAK groups.io #165551; SDR++ issue #1734] | same | |
| TX architecture | Non-linear switching PA of BS170 MOSFETs (gates driven by 5 V square wave via 74ACT08) driven by synth square wave + audio-rate **amplitude modulator** (P-MOSFET Q507 driven by 12-bit DAC). CW/Digi = constant-envelope single tone; SSB = polar modulation (see tx-audio-and-ssb.md) | same | op manual, ssbbeta.html |
| Extras | — | CR2032 RTC, internal QLG3 GPS option, AUX 3.5 mm jack (Serial 1), dev board | qmxp.html |
| RX current | ~80 mA | ~80 mA | qmx.html |

Consequences for HL2 emulation:
- Max usable RX bandwidth is 48 kHz (complex) and **only one receiver**. HL2 sample rates of
  96/192/384 kHz and multiple receivers must be faked (resample/zero-fill) or refused.
- No duplex: during TX the daemon should send zeros / mute to the HL2 client.
- "TX drive" in HL2 cannot be mapped linearly; closest is `MM Protection|Max. PA voltage` (V),
  and output power ~ V². It is a persistent EEPROM write (see cat-commands.md).

## 2. USB composite device

| Item | Value | Source |
|---|---|---|
| Connector | USB-C | op manual p.9 |
| VID:PID | **0483:A34C** (STMicroelectronics VID) | [field] SteffenLav/qmx-panadapter `main/cat/cat.c` |
| Product string | "QRP Labs QMX Transceiver" (sound card name in WSJT-X); Linux serial by-id `/dev/serial/by-id/usb-QRP_Labs_QMX_Transceiver-if00` | op manual p.91, p.100 |
| Audio | UAC (class-compliant, no driver on Win/Linux/macOS), stereo, **48 kHz, 24-bit** ("24-bit 110 dB 48 ksps"), both directions | op manual p.9, p.111 |
| Audio sample format on the wire | packed **S24_3LE**, 6 bytes/frame, L then R; 48 frames per 1 ms iso packet. Audio IN (radio->host) is iso endpoint **0x83**, interface 3 alt 1 | [field] SDRPlusPlus-iak `AndroidBackend.cpp`, `LinuxBackend.cpp`; SteffenLav `audio.c`; OK1IAK groups.io #165551 |
| Other sample rates | none documented; 48 kHz only [UNVERIFIED that no other alt settings exist] | |
| Virtual COM ports | CDC-ACM, **1, 2 or 3** ports (System config / GPS & Ser. Ports / "USB serial ports", since 1_02_000). Change requires QMX power cycle. 3 ports uses "ghost endpoints"; works Linux/Win10; manual also says Windows 11 only works with 2; Win7 not with 3 | op manual p.72-73, p.28 |
| Baud rate | irrelevant (virtual) | op manual p.91 |
| Port roles | All USB ports equivalent: CAT **or** terminal. A CR (0x0D) switches that port into the terminal/menu application | CAT manual p.1 |
| Extra UARTs | Serial 1 on AUX jack (QMX+ only), Serial 6 on PTT jack (needs hardware mod), 75-115200 baud | op manual p.73 |
| Bootloader | USB mass-storage device for firmware update | op manual p.9 |
| DTR | DTR of USB 1/2/3 can key CW ("Key from USB DTR", since 1_03_000) or SSB PTT ("PTT from DTR", since 1_04_000) | op manual p.28, p.47 |

OS notes:
- **Linux**: ModemManager may send AT commands to /dev/ttyACM*; a CR makes QMX enter terminal
  mode and spew characters. Disable ModemManager or blacklist the device (op manual p.90).
  ALSA: open with `SND_PCM_FORMAT_S24_3LE`, 2 ch, 48000 (SDR++-iak LinuxBackend).
- **macOS**: no drivers needed; CoreAudio exposes the device; CAT at `/dev/cu.usbmodem*`.
  SDR++-iak uses a HAL AudioUnit requesting Float32 at 48 kHz and needs microphone permission
  (TCC) to capture. [field]
- **USB wedge** [field]: SteffenLav reports that if the host disappears mid-stream (host reset,
  not cable unplug) the QMX may stop answering enumeration until QMX power-cycle (seen on
  1_03_002 and 1_04_004). Unresolved.

## 3. Firmware versions relevant to this project

| Version | Date | Relevant change |
|---|---|---|
| 1_00_016/017 | early 2024 | IQ mode existed but **disabled TX** while on (power cycle needed after disabling) |
| 1_00_021 | 25-Jul-2024 | KS, KY, RT/RC CAT |
| **1_00_024** | 06-Aug-2024 | **IQ mode now streams raw I/Q while the transceiver otherwise works normally (CW TX/RX retained)** |
| **1_02_000** | 02-May-2025 | **SSB TX/RX (polar modulation)**, up to 3 USB serial ports, CAT LC, SA, SM, PC, SW, SS |
| 1_02_001 | 11-Jun-2025 | CAT RU/RD relative mode |
| 1_02_002 | 12-Jun-2025 | OM, TB commands; AUX/PTT serial ports |
| **1_02_004** | 06-Aug-2025 | **CAT TA (transmit audio tone by CAT)**, KY TS-480 compat mode |
| 1_02_006 | 31-Oct-2025 | **MM/ML menu manager**, RG (RF gain), AG = AF gain |
| 1_03_000 | 06-Feb-2026 | DTR CW keying, PL command, SSB input "Auto" |
| 1_03_002 | 07-Feb-2026 | USB-audio monitor during SSB TX |
| 1_04_000 | 08-May-2026 | AM RX, Virtual U3S, KD, MU, TR, RR, PS, AI, MD8 (SWR tune), PTT from DTR, Symmetric phase |
| 1_04_001 | 12-Jun-2026 | fixes 1_04_000 on QMX (non-plus) |
| 1_04_003 | 18-Jul-2026 | BD, BN, BU, UI |
| 1_04_004 | 23-Jul-2026 | GP, SR; power meter full scale 6/12 W |
| 1_04_010 | 14-Sep-2026 | Stable |
| 1_04_011 | 19-Sep-2026 | beta; [field] broke DiGi CAT `TX;`/`TA` path (no RF) - fixed 1_04_012 "DiGi mode always reported insufficient audio" |
| 1_04_014 | 23-Sep-2026 | "SSB mode not keyed properly from CAT TX; RX;" fixed |
| 1_04_015 | 24-Sep-2026 | latest beta |

Recommendation: target **>= 1_04_010** (or 1_04_012+ if using beta), query `VN;` at startup and
refuse/warn on < 1_02_006 (no MM) or < 1_02_004 (no TA).

Hamlib: model **2057** `RIG_MODEL_QRPLABS_QMX` (Kenwood backend `ts480.c`), generic QRP Labs
2052. Firmware manual recommends TS-440/TS-480 profile.
