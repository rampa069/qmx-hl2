# Design considerations: QMX/QMX+ as an emulated Hermes-Lite 2 (openHPSDR Protocol 1)

Research date: 2026-09-27. Companion to `prior-art.md`.
**[V]** means verified from the QMX 1_02_000 manual, the HL2 wiki, or source code. **[U]** means
unverified, an inference, or needing a bench test. The "Open questions" list at the end collects
the [U] items that block design decisions.

Register addresses below follow the HL2 wiki convention: a 6-bit *address*, sent on the wire as
`C0 = (addr << 1) | MOX`. So "address 0x01 (TX NCO)" is C0 byte 0x02/0x03, "address 0x02 (RX1 NCO)"
is C0 byte 0x04/0x05, and so on.

Sample chain at a glance:

```
Host (Thetis/SparkSDR/piHPSDR/...)  <--UDP P1 (EP6 RX IQ, EP2 TX IQ + C&C)-->  daemon
daemon <--USB audio 48k/24-bit stereo (IQ in, audio out)-->  QMX
daemon <--USB CDC serial, TS-480-like CAT (FA, TQ, Q9, SS, ...)-->  QMX
```


> **Update (same day, after reading `qmx/iq-mode.md`, `qmx/tx-audio-and-ssb.md` and the CAT manual
> 1_04_004 in `qmx/ref/`):** several [U] items below are now settled or changed.
> - **CAT `TA<Hz>;` (since fw 1_02_004)** sets the Digi TX tone directly, with shaped key-down and
>   key-up. This is a better implementation of Option 2 than playing audio: compute `f_i` from TX IQ
>   and stream `TA` updates over CAT (`FA` → `TX;` → `TA…` → `TA0;` → `RX;`). It needs no audio
>   output, no 80% threshold and no playback drift servo for Digi/CW. It is reported to work with IQ
>   mode on [field]. A regression is reported in 1_04_011. The USB-audio route stays as the fallback.
>   The CAT update rate limits how fast the tone can change; FT8 needs about 6.25 Hz symbols.
> - **SSB via CAT is `MD2;` (USB) / `MD1;` (LSB)** in the 1_04_004 CAT manual; `MD6` is Digi.
> - **During TX the IQ stream continues but carries junk** (TX leakage), per the field reports in
>   `qmx/iq-mode.md`. EP6 must still be blanked or replaced with noise during TX. Keep the timer
>   fallback in case the stream stops.
> - **`Q9` is volatile.** `MU;`, menu access and power cycles drop it, so re-assert it and read it back.
> - The LO is 12 kHz below the dial in USB/LSB/DIGI (it has an extra CW offset in CW mode), so use
>   `FA = F_centre + 12000` in MD2/MD6.

---

## a) Converting HPSDR TX IQ into something the QMX can transmit

### What the host sends
EP2 frames carry 16-bit I/Q at a fixed **48 kHz** (plus L/R speaker audio, which we ignore) [V].
The TX NCO frequency `f_nco` arrives at address 0x01 (TX freq register) [V, HL2 wiki]. The RF
signal the host wants is `s_RF(t) = Re{ x(t) · e^{j2π f_nco t} }`, where `x = I + jQ` is complex
baseband. A tone at baseband frequency `f_bb` (which can be negative) lands at `f_nco + f_bb`.
Typical content by client mode [U: client-dependent]:

| Client mode | Baseband content `f_bb` |
|---|---|
| DIGU / USB | +200 … +3000 Hz (FT8 tone at, e.g., +1500 Hz) |
| DIGL / LSB | −3000 … −200 Hz |
| CW (host-generated IQ) | Usually a keyed carrier at or near 0 Hz (DC). Some clients offset it by ±pitch [U] |
| AM/FM | Around DC, both sides |

### What the QMX accepts from USB audio
- **Digi mode** measures the frequency of a *single* audio tone from zero crossings (about 100
  measurements/s by default) and synthesizes RF = dial + f_audio (USB) or dial − f_audio (LSB, via
  the `Q1` sideband setting). It keys only while the amplitude exceeds **80% of full scale** (Rise
  threshold) and unkeys below 60% [V]. Output power is always full; amplitude carries no information [V].
- **SSB mode** (firmware 1_02_000 or later) takes real audio from USB (`SS0;`), band-limits it (the
  "Filter TX" setting), and transmits true SSB through EER/polar modulation at 12 ksps [V].

### Option 1: Complex shift plus real part (the "analytic signal to audio" route)
`a(t) = Re{ x(t) · e^{j2π K t} }`, where `K = f_nco − f_dial` and the QMX is tuned to `f_dial`.

- For a single tone `x = A e^{j2π f_bb t}`: `a = A cos(2π (f_bb + K) t)`. This is a clean tone at
  `f_bb + K`, and the QMX in USB Digi mode transmits at `f_dial + f_bb + K = f_nco + f_bb`,
  **which is exactly right**, provided `f_bb + K > 0` (otherwise the tone folds to `|f_bb + K|` and
  the frequency comes out wrong) and `f_bb + K` stays inside the QMX's measurable range. The range
  is probably about 100 Hz to 3-4 kHz: at 4 kHz there are only 12 samples/cycle, and the manual
  warns that high frequencies may not reach the rise threshold every cycle [V/U].
- **USB** with `K = 0` (dial = NCO): `a = Re{x} = I`. For an analytic USB baseband this *is* the USB
  audio. This is correct for DIGU tones and for SSB voice (in QMX SSB mode). It is the trivial case.
- **LSB**: either (i) choose `K ≥ 3 kHz` so that the negative frequencies land at positive audio. This
  works for single tones, but voice ends up at an awkward 0-3 kHz offset and the QMX SSB TX filter
  would clip it. Or (ii), which is better: conjugate, `a = Re{ conj(x) · e^{j2π K' t} }`, and switch
  the QMX to LSB (`Q1 1` for Digi) with dial = `f_nco` (K' = 0). Then RF = dial − f_audio = f_nco + f_bb.
  This is correct. **The daemon cannot tell which sideband the client uses** (Protocol 1 carries
  only IQ), so it must detect the sign of the dominant frequency (see Option 2) or take a config/CAT
  hint.
- **Weakness**: amplitude follows the host's drive level. Unless the host drives near full scale,
  the QMX's 80% gate never opens. Normalization is required for Digi (see below).

### Option 2 (recommended for Digi/CW): instantaneous-frequency resynthesis
Treat the QMX as an FM exciter:
1. Compute the instantaneous frequency of the complex TX baseband:
   `f_i[n] = arg( x[n] · conj(x[n−1]) ) · 48000 / 2π`. Smooth it over about 1-5 ms, and gate it by
   `|x|` against a small threshold (for example −30 dBFS relative to the running peak).
2. Pick `f_dial` at key-down so that the audio lands in the QMX sweet spot (for example 1000-2000 Hz):
   `f_dial = f_nco + f_i,start − 1500`, rounded to 1 Hz. This needs the first few ms of TX IQ
   *before* we key the QMX, which suits us because we buffer anyway. Alternative: a fixed
   `f_dial = f_nco − 1500 Hz` for CW-style DC carriers and `f_dial = f_nco` otherwise.
3. Synthesize a **full-scale** (about 95% FS) phase-continuous sine at `f_audio = f_nco + f_i − f_dial`.
   Output silence when the gate is closed, so that the QMX unkeys.

Properties:
- The sign of `f_i` is preserved, so **USB and LSB are handled automatically**, with no need to know the
  client mode, and **CW as a DC carrier** becomes a 1500 Hz tone at the correct RF.
- It is amplitude-independent, so it always clears the 80% gate. The host's drive slider has no
  effect. The QMX cannot vary power anyway [V].
- FT8/FT4/WSPR/JS8/RTTY-FSK are all constant-envelope single-tone signals. Resynthesis is exact, and
  the QMX then re-measures a clean synthetic tone. It also removes the host's raised-cosine
  amplitude shaping, which the QMX ignores anyway (it uses its own threshold gating [V]).
- CW keying: ramp the synthetic tone's amplitude in about 1 sample (hard on/off). The QMX gate plus its
  own synthesizer define the RF envelope. Latency is ≥1 measurement period (10 ms by default, via
  `Q7` Minimum samples). **CW above about 25-30 WPM may show shortened dits [U]**. An alternative is to
  key the QMX's native CW via the serial port's RTS/DTR, if the QMX supports that [U].
- **Multi-tone or phase modes (PSK31, VARA, SSB voice) do not fit Option 2.** Detect them by the
  variance of `f_i` and by amplitude modulation, and fall back to Option 1 in QMX SSB mode.

### SSB voice
If the firmware supports SSB TX (1_02_000 or later [V]): put the QMX in SSB mode, USB audio source
(`SS0;`), sideband matching the sign of the dominant content, and send Option 1 audio (`Re{x}`, or
`Re{conj x}` for LSB) scaled so that peaks are about −1 dBFS. Caveats:
- The host's own SSB processing (compressor, TX EQ, CESSB) survives, but the QMX re-filters through its
  own TX filter (Filter TX setting) and EER at 12 ksps [V]. Expect QMX-typical quality, not
  host-quality. PureSignal is impossible because the QMX gives no feedback RX.
- The EER gate threshold ("Samples" / "Attack slope") mutes low-level audio [V]. Host ALC and drive
  settings matter here, unlike in Digi mode. Map HL2 drive level (address 0x09) to an audio gain [U].
- **How to select SSB mode via CAT is not documented in the 1_02_000 manual** (MD lists only 3/6/7/9)
  [V/U]. It may need `MD1;`/`MD2;` or a newer firmware.
- **Whether USB-audio TX works while IQ mode is enabled is [U]**. The manual only promises CW. The
  likely sequence per over is `Q90;` → select mode → `TQ1;` … `TQ0;` → `Q91;`, which costs extra CAT
  round-trips (a few ms each at 115200 baud) and an RX gap. The QMX pauses IQ during TX anyway [V].

### Mode-policy recommendation
Config `tx_mode = auto | digi | ssb | cw`. In `auto`: start in Digi resynthesis (Option 2). If
`f_i` variance and envelope variation indicate voice or multi-tone for more than about 200 ms, and SSB is
available, switch *on the next over*, not mid-over.

### PTT and safety
- MOX is bit 0 of C0 in every EP2 frame [V]. On a rising edge: pre-buffer (Option 2 step 2), send CAT
  `FA<f_dial>;`, then `TQ1;`, then start audio. On a falling edge: stop audio (silence first), then `TQ0;`.
- Enable the QMX CAT TX-timeout watchdog (`QB1;`, `QC<n>;`) and refresh it during TX, so that a
  crashed daemon or dropped UDP link cannot leave the radio keyed [V: commands exist].
- Honour HL2 PTT hang and TX latency (address 0x17 bits 12:8 and 6:0) [V, HL2 wiki] only loosely.
  They are timing hints, not hard requirements.

---

## b) Sample rate, clock drift, pacing and buffering

### Clocks involved
1. **QMX ADC/USB audio clock**: derived from the QMX's own oscillator or USB SOF. Nominally 48000 Hz,
   off by some ppm [U: exact USB audio sync mode of the STM32 implementation].
2. **Host clock**: the host has no rate of its own for RX. It consumes whatever arrives. For TX, most
   clients produce EP2 TX samples *in proportion to received EP6 samples* (48 kHz TX per 48k·n RX)
   [V for piHPSDR/hpsdrsim-style logic; U for Thetis internals].
3. **Daemon wall clock** (monotonic timer).

### Recommended pacing model: "the QMX is the master clock"
- **RX (EP6)**: emit EP6 packets *driven by the audio capture callback*, never by a timer. Concretely:
  collect QMX frames, run the DDC/resampler, and send a 1032-byte
  packet each time 2 × 504 bytes of sample payload are full. The EP6 rate then exactly equals the QMX
  clock × rate factor, which is what a real HL2 does (it is paced by its own 76.8 MHz crystal). Hosts
  cope with this by design.
- **TX (EP2)**: because hosts pace TX from received RX, TX IQ arrives at *the QMX input clock
  rate* (±host jitter). We play it to the QMX output, which almost certainly shares the device clock
  with its input [U]. Long-term drift is therefore about 0. Only jitter needs absorbing.
- **Still implement a drift servo for safety** (the host may pace TX from its own sound card, or the
  OS may insert its own resampler; CoreAudio does this for aggregate devices):
  - Target a TX ring fill of, for example, 40 ms. A PI controller on the smoothed fill level adjusts a
    resampling ratio within ±500 ppm. Use a Farrow/cubic or polyphase fractional resampler (drift
    corrections are tiny, so this is audibly and measurably transparent for FT8).
  - Simpler proven alternative: Quisk's approach of adding or dropping one sample every N samples
    when the average fill departs from 50% (`quisk/sound.c` `cr_correction`) [V]. In Option 2
    (resynthesis) drift is trivial: we synthesize from a frequency track, so we simply consume `f_i`
    frames at whatever rate and never need to resample audio.
- **During TX the QMX IQ stream pauses** [V/U]. If the capture callback stops or delivers silence, EP6
  pacing must **switch to a monotonic timer** (`clock_nanosleep` absolute deadlines, as hpsdrsim
  does [V]) and send zero or low-noise IQ. Otherwise hosts that pace TX from RX will starve the TX
  path, a **deadlock**. Hand pacing back to the capture clock when IQ resumes, and resync the buffers.

### Buffering and latency targets
| Stage | Target | Notes |
|---|---|---|
| Audio capture period | 5-10 ms (240-480 frames) | CoreAudio handles 5 ms. For Pi/ALSA, start at 10-20 ms. |
| EP6 packet | 126 samples at 48k = 2.6 ms | Fixed by protocol at 1 RX, 48k. It is 504/(6n+2) samples per 512-byte frame × 2. |
| UDP/host jitter buffer (TX) | 20-40 ms | Also HL2's own default TX latency is 20 ms [V]. |
| Audio playback buffer | 10-20 ms | |
| **Total TX latency** | **≈ 40-80 ms** | Irrelevant for FT8 (±1 s tolerance), fine for SSB. CW full break-in is not realistic. |
| RX latency | ≈ 10-20 ms + host | Dominated by the capture period. |

Also: request 24-bit capture and pass the 24 bits straight into EP6 (P1 carries 24-bit I/Q [V]), with no
requantization. Keep a monotonic sequence counter per EP6 and check EP2 sequence gaps for
diagnostics.

---

## c) Language and library choice

Requirements: macOS primary (CoreAudio), Linux/Raspberry Pi secondary (ALSA/PipeWire), 48 k stereo
capture and playback, UDP at about 380-3000 packets/s, one serial port, light DSP (NCO mixing, halfband
interpolation, FIR/resampler, `atan2` per sample), and a single static-ish binary for a daemon.

| Option | Audio | Serial | DSP | Pros | Cons |
|---|---|---|---|---|---|
| **Go** | PortAudio via `gordonklaus/portaudio` (cgo), or `gen2brain/malgo` (miniaudio, cgo) | `go.bug.st/serial` | Hand-written. Easy at these rates (a few MFLOP/s) | **The user already has a working Go audio+CAT+serial daemon (`uSDX/audioStreamer`) with cross-builds to static Linux/Windows and vendored PortAudio/ALSA [V].** Goroutines plus channels map naturally onto UDP / audio / CAT loops. `jancona/hpsdr` (Apache-2.0) gives Go P1 constants and framing. Easy to deploy on a Pi. | GC. But with Go 1.2x pause times (sub-ms) against a 10-40 ms buffer budget, this is not an issue at this load. cgo is needed for audio. Weaker DSP ecosystem. |
| **Rust** | `cpal` (CoreAudio/ALSA/JACK) | `serialport` | `rubato` (async resampler with adjustable ratio, MIT), `num-complex`, `rustfft` | Real-time safe with no GC. `rubato` solves drift resampling out of the box. `rustyHPSDR` (local, GPL) and hpsdr-emu's Rust skeleton serve as P1 references. | More code to write from scratch. No existing user codebase to reuse. cpal device-name matching and 24-bit handling have rough edges on macOS [U]. |
| **C** | miniaudio (single header, public domain/MIT-0) or PortAudio | libserialport (LGPL-3) or POSIX termios | liquid-dsp (MIT), speexdsp resampler (BSD) | Can lift logic from **hpsdrsim (GPL-3)** and Pavel Demin's server (MIT) almost directly. Tiny footprint on a Pi. | Manual memory and thread safety. Slower to develop. More cross-platform build fiddling. |
| **Python** | `sounddevice` (PortAudio) | `pyserial` | numpy/scipy | Fastest prototype. hpsdr-emu (no license) shows asyncio P1 framing. 48 kHz stereo in numpy blocks is only about 1% CPU. | Per-packet Python work at about 380-3000 UDP packets/s, a GIL shared between the audio callback and asyncio, and GC jitter mean **timing is fragile, especially on a Pi**. Packaging a daemon is harder. It is fine as a throwaway spike for validating the Option 2 TX idea against a real QMX. |

**Recommendation: Go.** Justification:
1. Maximum reuse of the user's own proven code: the PortAudio backend, serial discovery and
   reconnect, Kenwood CAT framing, TX ring buffer and pacer patterns, logging, platform split, and the
   Makefile cross-compiling to static Linux with vendored PortAudio/ALSA. That covers about half of the
   non-protocol plumbing.
2. The load is modest. Even a 384 kHz, 4-receiver EP6 stream is about 3000 packets/s, which Go handles
   easily. DSP per sample is tiny.
3. Concurrency (UDP RX/TX, audio callback, CAT, watchdog) is idiomatic with goroutines and channels.
   Keep the audio callback allocation-free and hand off through lock-free or preallocated ring buffers.
4. The protocol layer can borrow Apache-2.0 constants from `jancona/hpsdr`, and hpsdrsim (GPL) and
   Pavel Demin's server (MIT) can be used as behavioural references without copying GPL code.
   Alternatively, adopt GPL-3 deliberately if we want to copy hpsdrsim logic.

Choose **Rust** instead if hard real-time guarantees on a Pi Zero-class device become the priority.
Consider switching the Go audio backend to `malgo` (miniaudio) if PortAudio's CoreAudio latency or
device naming causes trouble [U].

---

## d) RX: a 48 kHz IQ source versus HL2 clients expecting 48/96/192/384 kHz and multiple DDCs

### What the QMX delivers
48 ksps complex, so about 48 kHz of spectrum centred on `f_LO = f_dial − 12 kHz` [V: two sources].
Expect a DC spur plus LO leakage at the centre, a finite image (no I/Q correction in IQ mode; the QMX
IF design deliberately avoids DC) [V], and anti-alias roll-off near ±24 kHz (usable maybe ±20 kHz [U]).

### Sample rate (address 0x00, C1 bits 1:0: 48/96/192/384k [V])
The client picks the rate and Protocol 1 has no NAK. We must deliver whatever is asked.
- **Recommended**: honour all rates by **integer upsampling** of the 48k stream (×2/×4/×8 via
  cascaded halfband FIRs, which are cheap), then shift to each DDC's NCO. The band outside the QMX's
  ±24 kHz is empty. Inject very low-level white noise there (about 10 dB below the QMX noise floor)
  so that client AGC and waterfalls don't show −∞ dB bands and noise-blanker statistics don't misbehave.
- At 48k the output equals the input with the NCO shift applied. Recommend 48k in docs, since clients
  like SparkSDR/Thetis will show more "dead" spectrum at higher rates.
- Advertise HL2 (board ID 0x06 [V]) with a plausible gateware version (for example 7.2 or later, [U]
  which clients check). Answer the HL2 ACK/response mechanism (C0 bit 7) for writes that clients
  wait on [V].

### Tuning strategy (the RX1 NCO at address 0x02 is the "centre" the client displays)
- Keep the QMX `f_LO` fixed and satisfy RX NCO changes **digitally** (complex mix by
  `f_rxN − f_LO`) while `|f_rxN − f_LO| + bw_display/2` stays inside roughly ±20 kHz. Only when RX1
  moves outside a hysteresis window do we retune the QMX over CAT (`FA` = new centre + 12 kHz, or with
  an offset, see below). Retuning costs a CAT round-trip and a short Si5351 glitch, so rate-limit it.
- **DC spur handling**, two choices:
  1. Put `f_LO = f_rx1` and run a DC-blocking notch (a high-pass on I and Q, or a running-mean
     subtractor). This gives the full ±24 kHz, but there is a hole or spur at the centre, which is also
     where users tend to tune.
  2. Offset `f_LO = f_rx1 ± 6-12 kHz` so that the spur sits off-centre. This loses a chunk of usable span
     on one side (the display then shows ±24 kHz but the QMX covers only −12…+36 or similar).
  Make it configurable. For FT8 at 48k, option 2 with a 6 kHz offset keeps the 3 kHz sub-band clean.
- The TX dial frequency is independent (see a). Restore the RX `FA` after TX. The QMX may treat FA
  as shared between RX and TX. Consider VFO B / split (`FB`, `SP`) [U].

### Multiple receivers (HL2 supports 1-12 in gateware; the discovery byte 0x13 reports the count [V])
- Advertise a small number (2-4) to keep clients happy. Each DDC is a complex NCO mix of the *same*
  48k IQ stream by `(f_rxN − f_LO)` followed by the rate conversion.
- Receivers whose passband is fully outside the QMX's ±20 kHz get noise-only data (or zeros) and a
  log warning. Only RX1 drives retuning. This is the same idea as rtl_hpsdr's "COPY" receiver [V].
- PureSignal / TX feedback receiver: not supported, since the QMX IQ pauses on TX [V/U]. Send noise.
- Diversity or 2-ADC features: not supported. Ignore the relevant C&C bits.

### Other C&C mappings (RX side)
| HL2 control | QMX mapping |
|---|---|
| LNA gain (address 0x0A [V]) | No QMX CAT equivalent in IQ mode [U]. Apply a digital gain in the daemon, or ignore it. |
| ADC overload bit in responses | Derive from 24-bit sample peaks (> −1 dBFS). |
| Forward/reverse power, temperature in responses | Map CAT `PC`, `SW` (poll about 5 Hz, and only in TX) to the HL2 telemetry fields. Fake the temperature. |
| Band filter / OC outputs | Ignore. The QMX switches its own LPFs by frequency. |

---

## Open questions (need bench tests or answers from QRP Labs)
1. Does Digi- or SSB-mode TX from USB audio work while `Q91` (IQ mode) is set? If not, what is the
   latency or glitch cost of toggling `Q90/Q91` around every over?
2. The exact IQ offset sign and value (12 kHz above or below the centre?) for each sideband setting (`Q1`) and mode.
   Is I on the left, and is the spectrum mirrored?
3. During TX, does the USB capture stream stop, deliver zeros, or keep delivering? This decides the
   EP6 fallback-timer behaviour.
4. How do you select SSB mode via CAT on current firmware?
5. Do the QMX USB audio IN and OUT share one clock? Measure long-term drift against the Mac's clock and
   between IN and OUT.
6. What is the upper and lower audio frequency limit of the Digi tone measurement? What is the CW keying
   latency?
7. How does each target client (Thetis, SparkSDR, piHPSDR/deskHPSDR, Zeus, Quisk) pace EP2 relative
   to EP6? Which gateware version and ACK features does each one require?
