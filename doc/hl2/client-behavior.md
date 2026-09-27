# How HL2 clients use Protocol 1, and what an emulator must do

Companion to `protocol1.md`, which has the wire format, register map and gateware citations.
This file covers what the main clients actually send and parse, how they pace TX IQ, and the minimal
feature set an HL2 emulator (QMX backend) needs to be accepted.

Clients examined, all local:

| Client | Path | Version | Key files |
|---|---|---|---|
| piHPSDR (DL1YCF) | `/Volumes/FastDisk/Radio/HL2/pihpsdr/src` | git 1d36271a | `old_discovery.c`, `old_protocol.c`, `radio.c`, `transmitter.c`, `hpsdrsim.c` |
| deskHPSDR (fork) | `/Volumes/FastDisk/Radio/HL2/deskhpsdr/src` | git 470d605 | same file names |
| Thetis, mi0bot HL2 fork | `/Volumes/FastDisk/Radio/HL2/OpenHPSDR-Thetis/Project Files/Source/` | git e3375d0 | `ChannelMaster/networkproto1.c`, `netInterface.c`, `network.c`; `Console/HPSDR/clsRadioDiscovery.cs`, `NetworkIO.cs`, `IoBoardHl2.cs`, `console.cs`, `setup.cs` |
| Quisk (N2ADR) | `/Volumes/FastDisk/Radio/HL2/quisk` | 4.2.51, git e6b9d9d | `hermes/quisk_hardware.py`, `quisk.c`, `microphone.c` |
| hermeslite.py | `/Volumes/FastDisk/Radio/HL2/Hermes-Lite2/software/hermeslite/hermeslite.py` | – | port-1025 setup tool |

Line numbers are for these checkouts. Thetis paths are relative to `Project Files/Source/`.
SparkSDR, SDR Console, linHPSDR and PowerSDR are closed or not present locally. They are
documented by the HL2 wiki as "compatible with openHPSDR P1" and are expected to behave like the above.
Test them empirically.

---

## 1. Discovery

| | piHPSDR / deskHPSDR | Thetis (mi0bot) | Quisk |
|---|---|---|---|
| Request | `EF FE 02` + zeros, **63 bytes** (1032 over TCP) to port 1024. Broadcast on each UP interface, or unicast to a configured IP (pi `old_discovery.c`:260-283). desk first sends a METIS **STOP** (`EF FE 04 00`), then the probe (desk `old_discovery.c`:252-270) | 63 bytes (`clsRadioDiscovery.cs`:1335-1343). Sent to the subnet broadcast, 255.255.255.255, and optionally a fixed IP, per NIC. Retries with profile defaults 2×4×100 ms (:465-511, 919-1113) | 63 bytes, broadcast plus 255.255.255.255, or unicast. 5 tries × 5×20 ms (`quisk_hardware.py`:225-251) |
| Reply check | `EF FE`, status 2 or 3 (pi:361-507) | len ≥ 24, `EF FE 02` or `03` (:1149-1162) | len > 32, `EF FE` (:255) |
| Status 0x03 | Listed, but the start button shows **"In Use"** and is disabled (pi `discovery.c`:817-820) | Marked `IsBusy` | – |
| Board id (byte 10) | 6 means Hermes-Lite. `software_version = 10*buf[9] + buf[0x15]`; **≥ 400 → HL2** (else HL1) (pi:410) | 6 → `HPSDRHW.HermesLite` (:1243). The model is actually picked by the user, and a mismatch only warns (`NetworkIO.cs`:153-186) | 6 plus hardware file "Hermes" → `is_HermesLite2` (:281) |
| Other bytes | MAC 3-8; 0x0B, 0x0D-0x12 logged only. Receivers hard-coded to 2 (pi:488) | MAC, 9, 0x15 (`BetaVersion`), 0x0B/0x0C, 0x0D-0x10. **Bug**: `NumRxs` is overwritten from byte 0x14 (:1191-1200) | **0x13 = receiver count** (clamped 1..10), 0x15 minor, 0x16[7:6] bandscope scale (the wiki puts this flag in 0x14, so it's a Quisk quirk) |
| Version gates | ≥ 400 composite → HL2 | none for HL2 | `ver < 40`: old LNA encoding. `ver ≥ 60`: bias/EEPROM I²C at 0xAC (`quisk_hardware.py`:636-642, 858) |

**Emulator:** answer 63- *and* 60-byte probes (hermeslite.py sends 60 bytes to port 1025),
unicast and broadcast. Reply with 60 bytes:
`EF FE 02 <MAC> 0x4A(74) 0x06 00 00 00000000 0000 <NR> 0x45 0x02 00…`.
* Reply with status 0x02 while idle. Use **0x03 only while streaming to a client**; piHPSDR will then refuse
  a second client.
* Byte 0x14 = 0x45: wideband 16-bit, build 5.
* Byte 0x13 (NR): the number of receivers the emulator supports. 1 is honest for a single QMX
  IQ stream, but see §6.
* Use a MAC in the HL2 OUI `00:1C:C0:…` or a locally administered one. Clients don't validate it.

## 2. Start / stop / priming

* **piHPSDR** (`old_protocol.c`:2851-2910): sends 2 EP2 packets (TX freq, then RX1 freq C&C, 20 ms apart),
  then `EF FE 04 01` (64 bytes). It waits for EP6 and retries up to 10×. **deskHPSDR** (:3358-3400)
  queues 4 zero EP2 packets, waits 100 ms, sends start and does not retry.
* **Thetis** (`networkproto1.c`:38-74, 111-144): `ForceCandCFrame(1)` (EP2 with TX freq, then RX0 freq),
  then `EF FE 04 01` (64 bytes), up to 5 tries. Success means an EP6 arrived within 500 ms. The HL2
  read loop re-primes with `ForceCandCFrame(3)` (:435). The EP2 sequence number is **never reset**.
  Stop is `EF FE 04 00` up to 5×, and it checks that EP6 stops.
* **Quisk** (`quisk.c`:3425-3517): Stop ×2, drain the socket, prime 4 EP2 packets, then Start (`01`, or
  `03` with bandscope) repeated **every 2 ms until EP6 arrives**. It closes with Stop ×2. It restarts the
  stream to change the receiver count.
* No client requires the wideband stream for normal operation. Quisk asks for it only if the bandscope is enabled.

**Emulator:**
* Accept Start/Stop of any length ≥ 4.
* Start is idempotent: repeated starts must not reset anything visible, apart from sequence = 0 on the first one.
* Accept EP2 before Start.
* Begin EP6 within a few ms of Start. Thetis gives up after 500 ms × 5.
* Stream to the source IP:port of the Start packet.

## 3. EP2 C&C: what each client writes

`protocol1.md` §5.3 has the register semantics.

### piHPSDR / deskHPSDR (`ozy_send_buffer`, pi `old_protocol.c`:1777-2789)
* **Frame 1 of every packet is always ADDR 0x00.** It carries the rate, OC bits (N2ADR filters), band-volts (the
  dither bit), duplex=1, `nrx-1`, and the antenna. Frame 2 rotates through commands 1..11:
  * 0x01 TX freq
  * 0x02.. RX freqs. nrx is 1-2, or 4 with PureSignal (DDC2 and DDC3 = PS feedback)
  * 0x09: drive (0,16,…,240, since only the top nibble matters, with the rest applied as IQ scaling), `C2|=0x08` PA
    enable, pi `C2|=0x04` when the PA is off, `0x10` ATU tune
  * 0x0a: **C4 = 0x40 | (gain_dB+12)**, 0 when TX with PA
  * 0x0b keyer
  * 0x0e: `C3 = 0xC0 | txgain`
  * 0x0f: C1 bit0 = internal keyer enable. **CWX is never set.**
  * 0x10 hang/sidetone
  * 0x11 PWM
  * 0x12 (skipped on HL2)
  * **11 = HL2 slot**: default **0x17: C3 = 20 ms PTT hang, C4 = 40 ms TX latency**. desk uses 12 ms for CW,
    falling back to 40 after an underrun (desk:2265-2287).
* The HL2 slot also runs an I²C sub-state machine:
  * An **IO-board probe with RQST**: `C0=0xFA` (0x3d | RQST), C1 = 07 read, C2 = 0xC1.
  * IO-board writes: `C0=0x7A`, address 0x1d, registers 0-4 TX freq, 11, 13, 14.
  * **Versa clock** reprogramming on 0x3c (`C0=0x78`, C2=0xEA) when the external 10 MHz setting changes.
* It never writes 0x39/0x3a/0x3b in the stream. deskHPSDR's reboot button sends `EF FE 05 7F 74 00 00 00 01` to **port 1025**.

### Thetis mi0bot (`WriteMainLoop_HL2`, `networkproto1.c`:874-1206)
* It sends **one register per 512-byte frame**, cycling idx 0..18. Each register comes round about every 25 ms. The order is:
  * 0x00
  * 0x01 TX
  * 0x02 RX0
  * 0x03 RX1
  * 0x0e
  * 0x04-0x08 (DDC2-6, several set to the TX freq for PS)
  * 0x09 drive/PA (C2 0x08 = PA enable)
  * 0x0a (**C4 = (31−att)&0x3F | 0x40**)
  * 0x0b
  * 0x0f
  * 0x10
  * 0x11
  * 0x12 (PS bit)
  * **0x17 (C3 PTT hang, default 12; C4 TX latency, default 20)**
  * **0x3a (reset on disconnect)**
* HL2 runs with **nddc = 4** (`console.cs`:8422-8427). Thetis asks for 4 receivers even for a single VFO.
* I²C items pre-empt the rotation: 0x3c/0x3d with the RQST bit set for reads and for ACK'd writes. They are used by the IO
  board (`IoBoardHl2.cs`: 0x3d, address 0x1d, version read from 0x41), Versa clock setup on connect
  (`console.cs`:28086-28093), and the I²C setup page.
* CWX: when `cw_enable`, **every** I and Q 16-bit word is replaced by `cwx_ptt<<3 | dot<<2 | dash<<1 | cwx`
  (:1252-1257). This is the CWX/"CW via I LSB" path, used by Thetis CWX and MIDI CW (`cwx.cs`:292).

### Quisk (`microphone.c`:838-947, `quisk_hardware.py`)
* It rotates **ADDR 0x00..0x10** (17 frames) in C0 order: 0x00 (rate, OC = filter RX/TX byte, band-volts, duplex, nrx),
  0x01 TX, 0x02-0x08 RX, 0x09 (C1 drive 0-255, PA bit 19, tune/bypass), 0x0a
  (`C4 = (dB+12)|0x40`, PS bit), 0x0e (`C3 = (dB+12)|0xC0`), 0x10 CW hang. 0x0f is left at 0, so no CWX.
* **One-shot queue with RQST (ACK)**, sent in the second frame at least 20 ms apart:
  * **0x17** (PTT hang, default 4; latency, default 10)
  * **0x39** watchdog enable/disable
  * **0x3a** reset-on-disconnect
  * 0x3b AD9866
  * 0x3c/0x3d I²C
* **Quisk waits for the ACK**: an ACK whose address is 0x3f means "not processed" and the write is resent (`quisk.c`:3642-3663).
  These are pushed once RX data starts (`quisk_hardware.py` HeartBeat :519-522).

### Summary: registers an emulator will see

| ADDR | Seen from | Emulator action |
|---|---|---|
| 0x00 | all | rate → EP6 cadence/format; nrx → EP6 layout; OC/band-volts → ignore, or map to a QMX band |
| 0x01 | all | TX frequency → QMX CAT (`FA`/split) |
| 0x02 (+0x03..0x08) | all | RX1 → QMX VFO. Extra DDCs: see §6 |
| 0x09 | all | drive top nibble → QMX power (QMX has fixed power, so maybe ignore or map to TX IQ scale); PA bit informational |
| 0x0a, 0x0e | all | LNA gain: ignore, or map to QMX RF gain/attenuator if available |
| 0x0b, 0x0f, 0x10 | all | CW keyer config: store it. 0x0f bit0 = "internal keyer" (pi/Thetis) means the *radio* keys CW from its own key jack |
| 0x17 | all | TX latency / PTT hang: use them as the emulator's TX jitter-buffer/hang parameters |
| 0x39, 0x3a | Quisk, Thetis | store; **ACK if RQST** |
| 0x3b-0x3d | pi/desk (IO board, Versa), Thetis (IO board, Versa, I²C page), Quisk | accept. **If RQST: ACK** (echo for writes; for reads return 0 data, or ADDR 0x3f "error" so clients conclude there is no IO board) |

## 4. TX IQ timing: who paces EP2?

**In every client, EP2 packets are generated at the same rate as the radio's EP6.** None of them has a free-running timer.
* **Quisk** is the most explicit (`quisk.c`:3545, 3620-3623; `microphone.c`:775-825). Each received EP6 adds
  `2*num_records` RX samples. One EP2 is sent for every `126 × (rate/48000)` RX samples. At 48 kHz with 1 RX
  that is exactly 1 EP2 per EP6.
* **Thetis**: the send thread blocks until both a 126-sample block of RX audio (L/R) and a block of TX IQ are
  ready (`networkproto1.c`:1223-1225; `network.c`:1288-1344). Both are produced by DSP driven by EP6 IQ and EP6 mic
  samples. With nddc = 4 that works out to 2 EP2 per 3 EP6 (19 samples per frame).
* **piHPSDR**: RX audio (during RX) and WDSP TX output (during TX) fill a ring. Every 126 samples it sends one EP2,
  with a burst-smoothing estimator of the radio FIFO: it assumes the radio drains 48000/s and sleeps 0.5-2 ms when
  "ahead" (pi:291-346). TX DSP input is the EP6 mic stream decimated to 48 kHz. deskHPSDR on macOS uses
  absolute-time pacing at `126/(48000·div)` s.
* EP2 packets flow **continuously, during RX as well**. They carry C&C, zero IQ with MOX = 0, and L/R audio that is zero
  on HL2 in pi/desk and RX audio in Thetis. This keeps the HL2 watchdog fed.
* **No client closes a loop on the HL2 TX-FIFO count** (EP6 addr 0 C3):
  * Quisk only counts under/overflow errors (`quisk.c`:3682-3719: after MOX it waits for a nonzero count, then flags `C3 == 0x80 || C3 == 0xFF`).
  * piHPSDR flags `0x80` underflow and `0xC0` overflow for display only.
  * Thetis ignores C3 and C4.
* TX IQ content:
  * piHPSDR/deskHPSDR **mask the I and Q LSB (`& 0xFE`)** so CWX can never fire by accident (pi:1732-1745).
  * Scale is ±32767. There is no power-dependent amplitude in Thetis, which disables its fixed gain. piHPSDR folds the remainder of the drive level below the 16-step nibble into IQ amplitude.
  * **I/Q ordering differs.** Quisk stores imag first (swapped) on both TX and RX (`microphone.c`:766-767; `quisk.c`:3748-3749). piHPSDR and Thetis don't swap.
    All of them produce correct sidebands on a real HL2, so the HL2 convention is self-consistent. The emulator must
    implement **exactly the HL2 convention** (as `protocol1.md` §5.1/§6.1 describe) and verify it against one client with an
    USB-mode tone.
* After TUNE or two-tone, piHPSDR waits 100 ms before dropping MOX so the FIFO flushes. deskHPSDR has a "TX fence"
  that waits for a zero block to be sent before unkey.

**Emulator consequences**
1. The emulator's EP6 cadence is the system clock. Generate EP6 from real QMX RX audio frames (48 kHz IQ over USB audio),
   and TX IQ will arrive at the QMX's own 48 kHz. Drift between the QMX capture and playback clocks is negligible
   (same device), but **host scheduling jitter of several ms is normal**. Buffer TX IQ for roughly the client-set
   latency (0x17 C4: 20-40 ms) before keying the QMX audio output. This mirrors HL2 PRETX.
2. If the emulator offers 96/192/384 kHz, it must resample the QMX 48 kHz IQ up to that rate. The QMX RX
   bandwidth is limited, so the extra rate only shows empty spectrum edges. Simplest: support all four rates
   (clients default to 48 k or 192 k), with the EP6 packet rate following the rate.
3. Only frames with **MOX = 1** contain valid TX IQ (HL2 discards the rest). Clients send zeros otherwise.

## 5. PTT / MOX / CW

* **MOX (host → radio)**: C0 bit0 in **every** EP2 frame while transmitting (pi:2753-2774; Thetis :901; Quisk
  `hermes_mox_bit`). In CW with the *internal* keyer enabled, pi/desk **do not set MOX**; the radio keys itself from its own key jack.
* **Radio → host C0 bits** (EP6, ACK = 0 frames):
  * **bit0 PTT**: pi/desk call `ext_radio_set_mox` on change, Quisk uses it as `hardware_ptt`, and Thetis polls it (`PollPTT`).
    So reporting PTT = 1 makes the client go into TX, following the radio.
  * **bit2 dot / bit1 dash**: pi/desk feed them to the host keyer if the internal keyer is off. Quisk
    reads bit2 as the hardware CW key. Thetis has a shift bug so it never sees them (`networkproto1.c`:502-503).
  * Quisk works around the HL2 CWX quirk. When a hardware key goes down it enters HARDWARE_CWKEY with **MOX = 0**,
    and the HL2 keys itself (`quisk.c`:5831-5837).
* **CW methods in use**:
  1. Host-generated CW as IQ with MOX = 1. This is pi/desk's default when the internal keyer is off, and also covers CAT/MIDI CW. Quisk does this for software keys.
  2. Radio-internal keying from its own jack, with MOX = 0. The host sees PTT/dot bits.
  3. Thetis CWX (key bits in the IQ LSBs, MOX = 0, 0x0f bit 24).
* **For a QMX backend**: (1) is natural: play the IQ as audio, and the QMX in digital/SSB TX mode turns it into RF. The
  QMX's own CW keying (paddle on the QMX, internal keyer) corresponds to (2): report PTT = 1 (and dot) in EP6 while
  the QMX transmits, so the client mutes RX and shows TX. (3) would need decoding the LSBs and keying the QMX via CAT.
  This is optional but low-latency.

## 6. EP6: what clients parse

| Field | pi/desk | Thetis | Quisk |
|---|---|---|---|
| Header / seq | 1032 bytes, `EF FE`, EP 6. Seq ≠ last+1 prints "SEQ ERROR" and a counter (seq 0 = restart) (pi:828-846) | 1032 bytes, EP 6. Resequencer (`pro.c`); `SeqError++` | 1032 bytes, sequence checked (`quisk.c`:3620-3630) |
| Samples | `504/(6n+2)` per frame, 24-bit | same (nddc = 4 → 19/frame) | same |
| addr 0 | C1 b0 overload, IO bits; C3 FIFO flags (display); C4 version (log) | C1 b0 overload only | C1 b0 overload, **b1 = TX-inhibit (active low: must be 1 or TX is inhibited!)**, C3 FIFO error watchdog |
| addr 1 | C1:C2 "exciter" = HL2 temp, C3:C4 fwd | C1:C2 temp (`T = (3.26·adc/4096 − 0.5)/0.01`), C3:C4 fwd | temp, fwd |
| addr 2 | rev, C3:C4 current | rev, current (`((3.26·adc/4096)/50)/0.04/(1000/1270)` A) | rev, current |
| ACK (C0 b7) | only an IO-board probe answer (0x3d, `F1F1F1F1` → IO board present) | I²C read data | **all queued writes (0x17, 0x39, 0x3a, …)** |
| mic | used as TX audio source if "radio mic" | used (decimated) | used if configured |

**Important details:**
* **Addr 0 C1 bit1 must be 1** (the HL2 sends `~txinhibit`). Quisk sets `tx_inhibit = !(C1 & 0x02)`, so an
  emulator that sends C1 = 0 would inhibit TX in Quisk. Send **C1 = 0x1E** (bits 4:1 set) as the idle value, like the real gateware.
* **Thetis nddc = 4, PureSignal nrx = 4 in pi**: the emulator must fill all requested receivers. Recommendation:
  advertise NR = 4 (like a real HL2), and fill every DDC whose NCO equals the QMX tuning with the QMX IQ. Fill other
  DDCs with zeros or a low noise floor. Thetis will not work with NR = 1, because it always requests 4.
* EP6 C4 of addr 0 = gateware version (74). pi logs it.

## 7. Watchdog / keepalive

* No client sends an explicit keepalive. The continuous EP2 stream is the keepalive. Only Quisk touches the watchdog
  register (0x39, if configured). Thetis optionally sets 0x3a reset-on-disconnect.
* Client-side timeouts:
  * Thetis read loop: 3 s. After that it tears down the resequencer; there is a latent NULL-deref if packets resume (`networkproto1.c`:448-455).
  * pi: 100 ms socket timeout, no restart.
* **Emulator:** stop streaming after about 10 s without EP2 (like the HL2), and **drop TX after about 100-200 ms without EP2 while MOX was set**.
  The real HL2 drops after latency + hang ≈ 32 ms once its FIFO underruns.

## 8. Minimal subset for acceptance

Must:
1. UDP 1024. Discovery reply (60 bytes) with **board id 0x06, version 74 (byte 9), minor ≥ 1 (0x15), NR = 4 (0x13)**, status 2/3.
2. Start/Stop, with the stream sent to the Start source. Repeated Starts are harmless.
3. EP6 1032-byte packets:
   * 32-bit sequence number
   * 2 frames with `7F7F7F`
   * rotating C0 addresses 0-3, with **addr 0 C1 = 0x1E | overload, C3 = FIFO status, C4 = 74**
   * correct `(6n+2)` round layout and zero padding for n = 1..4 (ideally up to NR)
   * paced at `rate / samples-per-packet`, locked to the QMX RX audio clock
4. Parse EP2 headers and both frames' C&C. MOX from C0 bit0. TX IQ only from MOX frames. Frequencies (0x01, 0x02), rate and nrx (0x00).
5. ACK responses for any C0 with bit 7 (RQST): echo ADDR and data (or ADDR 0x3f for unsupported I²C reads).
   Quisk depends on this.
6. Watchdog: stop without EP2. TX safety timeout.

Should:
* 0x17 latency/hang semantics
* PTT/dot reporting from QMX state
* fwd/rev/temp/current telemetry
* mic zeros
* accept 0x0b/0x0f/0x10 CW settings
* 60-byte discovery probes
* port-1025 discovery/command (for hermeslite.py and the deskHPSDR reboot button: answer 0x3a without actually rebooting)

Can skip:
* EP4 wideband
* ASMI flash programming (`EF FE 03`); **must not pretend success**
* AD9866 SPI, Versa clock
* PureSignal feedback: without a feedback path, clients will just fail to converge. Advise users to keep PS off.

## 9. Existing emulators and simulators in these trees

| Emulator | Path | Notes for reuse |
|---|---|---|
| **hpsdrsim** (DL1YCF, C) | `pihpsdr/src/hpsdrsim.c` (2060 lines; deskHPSDR copy `deskhpsdr/src/hpsdrsim.c`) | The best reference. `-hermeslite2` gives a reply with `[9]=73, [10]=6, [0x13]=4, [0x15]=2`. EP6 paced by absolute-time `clock_nanosleep`, `2·n/(48k<<rate)` s per packet (:1667-2027). It parses all C&C including HL2 0x09/0x0a/0x0e/0x17 (:1339-1665). It loops TX IQ back into RX as a feedback/PS signal. Limitations: **only 63-byte discovery probes, no ACK responses, FIFO count hard-coded to 0**, no port 1025. A ready test harness: run a client against it to compare with the emulator. |
| Zeus VirtualRadio (C#) | `zeus/Zeus.VirtualRadio/P1/*`, host `Zeus.VirtualRadio.Host` | Targets ANAN-10E (board 0x02). HL2 is allowed but generic. Discovery bytes 0x13/0x15 are 0. EP6 addr 0 is never sent. TX IQ is ignored. Stopwatch pacing. |
| Arion MockHl2 (Rust) | `arion/crates/hpsdr-net/src/mock.rs` | Test fake. Receiver count wrongly at byte 0x14. Fixed 1 ms packet cadence. Ignores EP2. |
| Saturn P1_app (C) | `Saturn/sw_projects/P1_app/p1app.c` | Hardware bridge that poses as a 4-RX Hermes (board 1). EP2 unimplemented. Paced by the hardware FIFO. |

No emulator implements ACK replies, a realistic TX FIFO status, or port 1025. The QMX emulator should.

## 10. Known client bugs and quirks that affect an emulator

* Thetis discovery overwrites `NumRxs` from byte 0x14 (`clsRadioDiscovery.cs`:1191-1200). This is harmless because Thetis uses nddc = 4 anyway.
* Thetis never sees EP6 dot/dash (shift bug, `networkproto1.c`:502-503). Only PTT works.
* Thetis never resets its EP2 sequence number. HL2 ignores it, and so should the emulator.
* Thetis ignores the I²C 0x3f error path (`returned_address` is never set), and an I²C queue can stall for about 64 frames after idling (signed `char delay`).
* Quisk reads the bandscope format from 0x16[7:6] instead of 0x14.
* piHPSDR hard-codes 2 supported receivers for P1 but uses 4 with PureSignal on HL2.
* hpsdrsim rejects 60-byte discovery. The real HL2 accepts any length, and the emulator should too.
