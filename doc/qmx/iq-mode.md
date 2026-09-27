# QMX IQ ("SDR") mode

Legend: **[doc]** QRP Labs documentation; **[field]** third-party code / forum; **[UNVERIFIED]**.

## Enabling

- Menu: System config -> **IQ Mode** ENABLED/DISABLED (op manual 1_04_004 p.71). Stored in EEPROM.
- CAT: **`Q91;`** enables, **`Q90;`** disables, **`Q9;`** reads (`Q91;`/`Q90;`). Session only,
  not written to EEPROM [doc, CAT manual p.8].
- Takes effect immediately, no reboot (Hans Summers + N6HAN, groups.io #126980/#127004, Aug 2024).
- **Q9 is fragile session state** [field, SteffenLav/qmx-panadapter `cat.c`]:
  - `MU;` (config reload) drops IQ mode -> re-send `Q91;` after any `MU;` or MM write with
    "MM effect = Immediate".
  - Entering/leaving the radio's own menu (front panel or terminal) can drop it.
  - QMX power cycle clears it (USB audio may survive the restart, so the host doesn't notice).
  - One user's 1_04 unit silently ignored `Q9 1;`. Always read back with `Q9;` and verify.
  - Reported asynchronous echo of set commands (`Q91;` coming back) — conflicts with SDR++-iak
    code comment that setters return nothing. Treat any unsolicited `Q9x;` as a status update
    and wait ~150 ms before issuing the verification query. [UNVERIFIED which is right]

## Stream format

| Property | Value | Source |
|---|---|---|
| Transport | Same USB Audio device used for normal demodulated audio; in IQ mode it carries raw ADC samples instead | [doc] CAT manual Q9, op manual p.71 |
| Rate | 48 000 samples/s complex | [doc] PCM1804 @ 48 ksps |
| Format | signed 24-bit, packed little-endian (S24_3LE), stereo, 6 bytes/frame | [field] SDR++-iak, SteffenLav |
| Channel order | **Left = I, Right = Q**; complex sample = L + jR gives correct (non-inverted) spectrum | [field] SteffenLav `rx_audio.c` NCO mix `(I + jQ)`, SDR++-iak `push(L, R)`; lloydm.net ("left and right stereo channels") |
| Packetisation | 1 ms iso packets of 48 frames (288 bytes) | [field] OK1IAK #165551 |
| Bandwidth | ±24 kHz around the LO (48 kHz total). The analog anti-alias filtering before the ADC is limited ("some limited low pass filtering") so edges alias/roll off [doc p.86] — usable ~±20 kHz [UNVERIFIED] | |
| DC | QSD/ADC DC offset at 0 Hz (LO); the IF design puts the dial 12 kHz away from DC | |

Beware host-side frame misalignment: if a read ends mid-frame and the remainder is dropped,
I/Q swap or garbage results for the rest of the session [field, SteffenLav `audio.c`].
Keep a byte carry-over buffer.

## Where the LO sits

The receive LO (synth output feeding the QSD) is **12 kHz below the dial** in SSB/DIGI/AM modes.

```
LO = dial - 12000                      (USB, LSB, DIGI, AM)
LO = dial - 12000 - cw_offset          (CW;  cw_offset default 700 Hz, read with MMCW|CW offset;)
LO = dial - 12000 + cw_offset          (CW-R)
RIT: effective dial = dial + RIT
```

Evidence: CAT manual `PL;` example (dial 28 060 000 DiGi -> synth 28 048 000) [doc];
"LO sits 12 kHz below the dial, so the 48 kHz covers dial−36 kHz to dial+12 kHz" [field,
SteffenLav/pd4hs README]; SDR++-iak `SYNCHRONIZATION.md` formula above [field]; "tuned
frequency 3/4 from the bottom of the IQ band" (lloydm.net, groups.io #120772). LSB = same
+12 kHz offset per SDR++-iak (the internal superhet uses the same LO and picks the sideband
in DSP) [field; UNVERIFIED on hardware by me].

**For HL2 emulation:** the HL2 client commands an RX NCO frequency F_c that it expects in the
center of the IQ stream. Set `FA(F_c + 12000);` in USB/DIGI mode (MD2 or MD6) so that LO = F_c,
then pass I/Q through. The dial ends up at +12 kHz from center, which is harmless. Avoid CW
mode for RX (extra offset) or compensate. Split (`SP1;` / FR/FT) lets TX VFO differ from RX VFO
(OK1IAK's approach, groups.io #165551).

## Frequency changes

- `FA<Hz>;` works in IQ mode; no documented retune blanking. Retune causes a glitch in the
  stream (synth PLL reprogram) [UNVERIFIED size].
- VFO limited to configured band edges unless "RX outside band" = ON; band switching
  (BPF/LPF) is automatic based on the band table when FA crosses into another band.
- `FA` outside all configured bands -> error / ignored [doc BN; UNVERIFIED for FA].
- CAT round trip < 50 ms [field pd4hs README]; SDR++-iak polls `IF;` every 100 ms, SteffenLav
  polls every 50 ms without problems.

## Image / I-Q balance

- Firmware applies **no** I/Q amplitude/phase correction (op manual p.111: "no attempt is made
  to compensate"). Raw stream has the analog imbalance: expect roughly 35-50 dB image rejection
  raw [UNVERIFIED — the 60-75 dB figures are for the internal superhet whose image is 24 kHz
  away and filtered]. The image of a signal at +f appears at −f around the LO.
- Host should implement adaptive IQ balance (SteffenLav implements `iq_balance.c`).
- 160 m quadrature was broken on QMX+ until 1_04_007.

## TX while in IQ mode

- Pre-1_00_024: TX disabled in IQ mode (Diagnostics reason "IQ mode is enabled").
- **Since 1_00_024: CW TX/RX works normally in IQ mode** [doc fw log + Hans groups.io #107749436].
- Digi TX via **CAT `TX;` + `TA<Hz>;` works with IQ mode on** [field: SteffenLav Tab5
  transmits FT8/WSPR that way with Q91 set].
- **Digi TX from USB audio (cycle counting) and SSB from USB audio while IQ mode is on:
  undocumented [UNVERIFIED]** — the host->radio audio direction is a separate endpoint and is
  probably still consumed, but not confirmed. Test on hardware; fallback is `Q90;` during TX.
- During TX the IQ stream **continues but contains junk** (TX leakage, DC pumping on Q,
  "keyed white noise"); no marker for TX start/stop [field OK1IAK #165551; SDR++ issue #1734
  "IQ data pauses ... weird screeching"]. The daemon must blank/mute IQ to the client during
  TX using its own TX state (it knows when it sent `TX;`), plus a margin of a few ms.
- Sidetone is not in the IQ stream (only on the earphone jack).

## Latency

- Radio internal RX latency ~15 ms (demodulated audio, qmxp.html). Raw IQ path bypasses the
  DSP chain, so it should be USB-buffer limited (~1-3 ms) [UNVERIFIED].
- Host audio stacks add their own buffering; SDR++-iak Linux uses 256-frame periods.

## Known limitations summary

1. 48 kHz only, one receiver, no wideband.
2. No IQ correction; image at mirror frequency.
3. Q9 state is volatile — must be re-asserted and verified.
4. No RX during TX; IQ garbage during TX.
5. Not usable simultaneously with demodulated USB audio (WSJT-X etc.) — it replaces it.
