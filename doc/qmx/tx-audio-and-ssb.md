# QMX transmit paths: Digi (tone measurement), CAT TA, SSB, CW

Legend: **[doc]** QRP Labs documentation; **[field]** third-party; **[UNVERIFIED]**.
Firmware references are to operating manual / CAT manual rev 1_04_004 unless stated.

## 1. DiGi mode (MD6 / MD9): audio -> single tone

- QMX does **not** modulate the audio. It measures the frequency of the incoming USB audio
  by zero-crossing / cycle-period counting and sets the synth to dial + f_audio, producing a
  pure constant-envelope single carrier (no sideband, no carrier leak, no IMD) [doc qmx.html,
  op manual Digi menu p.37-40].
- Parameters (Digi menu, and CAT Q4-Q8, QJ session overrides):
  | Param | Default | CAT | Meaning |
  |---|---|---|---|
  | Rise threshold | 80 % | Q4 | key-down only when audio amplitude > 80 % of full scale |
  | Fall threshold | 60 % | Q5 | key-up when below |
  | Minimum cycles | 1 | Q6 | min cycles per measurement |
  | Minimum samples | 480 | Q7 | min samples per measurement (480 = 10 ms -> 100 updates/s) |
  | Discard samples | 1 | Q8 | first zero-crossings ignored |
  | TX shift thrshld | 0 mHz | QJ | min change before retuning synth |
  | Sideband | USB | Q1 | |
  | VOX | OFF | Q3 | audio keys TX without CAT |
- **Audio level: must be near full scale** (>80 % peak). Manual: set app, device and master
  volume all to 100 %. Too low -> "E" flashes, TX status "two dots", no RF [doc p.94-95].
- Suited to all single-tone FSK modes (FT8/FT4/WSPR/JS8/RTTY/Olivia). **Not** suited to
  multi-tone or phase modes (PSK31, VARA, MSK144): the frequency counter produces garbage
  for a sum of tones; use SSB USB mode for those [doc].
- PTT: CAT `TX;` / `TQ1;` then audio; `RX;` / `TQ0;` to end. VOX optional (not recommended;
  system sounds get transmitted). CAT timeout (QB/QC) returns to RX if no CAT traffic.
- Update granularity 10 ms by default, so fast FSK is fine; tone-change timing latency
  roughly 10-20 ms plus USB/OS audio buffering [UNVERIFIED].

## 2. CAT `TA` — tone set directly (since 1_02_004)

Best path for an HL2 emulator that decodes the client's TX IQ into an instantaneous frequency
when the signal is single-tone:

```
FA<dial>;  TX;  TA<f_audio>;  TA<f2>; ...  TA0;  (wait ~5 ms)  RX;
```

- TF = dial + f_audio (in DIGI/USB), fractional Hz allowed (`TA1502.34;`).
- First TA keys down with Blackman-Harris shaped envelope; `TA<10` keys up shaped.
- `RX;` without TA0 = hard (unshaped) key-up.
- [field] Tab5 sends FT8 at 160 ms cadence; `TA` must be in DiGi mode. 1_04_011 broke this
  (no RF), fixed in 1_04_012.
- Limitation: constant amplitude, one tone at a time. Arbitrary IQ (SSB voice, PSK) is not
  possible through TA.

## 3. SSB (since 1_02_000, stable; beta was 1_01_x)

- Modes `MD1;` LSB, `MD2;` USB. Also the path for PSK/VARA/multi-tone digital.
- **Method: polar modulation (EER, Kahn).** Baseband audio at 12 ksps -> Hilbert -> polar
  (phase, magnitude). Phase applied as rapid frequency updates to the MS5351M synth; magnitude
  applied via 12-bit DAC to the PA supply amplitude modulator (interpolated x28). Non-linear PA
  [doc ssbbeta.html, op manual p.45-51, p.117].
- **Input source** (SSB menu "Input", CAT `SS`): `SS0;` = USB sound card from PC,
  `SS1;` = internal two-tone 700+1900 Hz, `SS2;` = external mic. Menu also has **Auto**:
  mic normally, USB audio when TX was initiated by CAT `TX;`/`TQ1;` (since 1_03_000).
- USB audio input: 48 ksps, downsampled to 12 ksps internally; "full amplitude" expected; no
  EQ/AGC/compression applied to USB audio (CESSB and TX filter still apply). "USB Audio > Gain"
  (dB) and "Monitor" (earphone monitor) settings [doc].
- TX filter 2500/2700/2900/3200 Hz; CESSB (on by default); phase and amplitude pre-distortion
  with self-calibration (dummy load) [doc].
- **Quality**: two-tone IMD3 about −39 to −40 dB PEP on 40 m with pre-distortion [doc
  ssbbeta.html]. Amplitude control range only ~37 dB (≈1 Vpp leakage floor at zero amplitude),
  so low-level components/noise produce phase noise; noise gate exists for mic. OK for voice
  and robust digital; worse than a linear SDR TX for weak-signal multi-tone modes.
- PTT: CAT `TX;`, PTT jack, VOX (on USB audio too), or DTR ("PTT from DTR", 1_04_000).
  "PTT to TX delay" setting. 1_04_014 fixed "SSB mode not keyed properly from CAT TX; RX;".
- **For HL2 emulation:** client TX IQ (48 kHz) can be converted to a real USB-sideband audio
  signal (shift down so the carrier is at dial, take real part / SSB demod) and played into
  the QMX USB audio output with MD2 + SS0 + TX;. Arbitrary IQ bandwidth is limited to the TX
  filter (≤3.2 kHz) and to the passband 0..3.2 kHz above the dial (USB).

## 4. CW

- `KY <text>;` sends text via internal keyer at `KS` speed (80-char buffer; `KY;` status;
  TS-480 compatibility mode switch in menu). Prosign chars documented [doc].
- `KD1;` / `KD0;` key down/up (since 1_04_000; CW only, returns `?;` in DiGi [field]).
  Usable as a straight-key over CAT, but timing jitter = CDC + host scheduling (ms-level);
  no latency figures published [UNVERIFIED].
- DTR keying: CW straight-key from DTR of USB 1/2/3 ("Key from USB DTR", 1_03_000; fixed for
  ports 2/3 in 1_03_002/1_04_000). No RTS keying documented.
- There is no documented "stop keyer/flush" command; `RX;` does not kill the keyer buffer
  (OK1IAK request, groups.io #165551).
- CW works in IQ mode (since 1_00_024).
- For HL2 CW (client sends key bits in the EP2 frames): use `KD1;/KD0;` or DTR. DTR edge is
  probably lower latency than a CAT command [UNVERIFIED].

## 5. Power control

- No CAT "set power" (Kenwood PC is **read-only**: measured watts ×10).
- Use Menu Manager: `MMProtection|Max. PA voltage=<V>;` (EEPROM write, persistent; reload via
  MU; or "MM effect = Immediate"). Output ~ V², below ~6 V ≈ 1 W, below 1 V no further effect.
  Reducing it degrades SSB dynamic range [doc p.63-64]. Tune mode has its own "Tune PA
  voltage" percentage.
- Readback: `PC;` (0.1 W units, 3 digits ≥10 W since 1_04_000), `SW;` (SWR ×100; only valid in
  TX, returns `SW;` in RX), `SR;` SWR protection latch. Power meter full scale 6 W / 12 W
  setting (1_04_004); hamlib reads it via MM.
- Note: EEPROM wear — avoid writing Max. PA voltage on every HL2 drive change; quantise and
  only write on real changes [recommendation].
