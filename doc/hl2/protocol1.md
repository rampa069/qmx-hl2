# Hermes-Lite 2 — openHPSDR Protocol 1 ("old protocol") wire specification

Implementation-grade reference for emulating an HL2 on UDP port 1024. Everything
here was checked against the HL2 gateware RTL; where the wiki and the RTL disagree,
the RTL is given and the disagreement is noted.

## Sources and citation keys

| Key | Path | Notes |
|---|---|---|
| **WIKI** | `zeus/docs/references/firmware/hermes-lite-2/wiki/Protocol.md` (copy: `ref/hl2-wiki-Protocol.md`) | softerhardware HL2 wiki "Protocol" page |
| **USB** | `ref/USB_protocol_V1.60.txt` (from `zeus/docs/references/protocol-1/USB_protocol_V1.60.doc`) | openHPSDR USB protocol V1.60, the base 512-byte frame format |
| **METIS** | `ref/Metis-How_it_works_V1.33.txt` (from `OpenHPSDR-Firmware/Protocol 1/Documentation/`) | Metis UDP encapsulation, discovery, start/stop |
| **DS** | `gateware/Original/gateware/rtl/dsopenhpsdr1.v` | Parses host→radio packets. Upstream softerhardware repo, commit `7472bd1` (2025-11-24), gateware 74.2 |
| **US** | `…/gateware/Original/gateware/rtl/usopenhpsdr1.v` | Builds radio→host packets (EP6, EP4, discovery) |
| **CTL** | `…/gateware/Original/gateware/rtl/control.v` | Response rotation, ACK FSM, PA/TR, CW input |
| **RADIO** | `…/gateware/Original/gateware/rtl/radio_openhpsdr1/radio.v` | NCOs, TX state machine (latency, PTT hang, CW) |
| **FIFO** | `…/gateware/Original/gateware/rtl/fifos.v` | TX IQ FIFO (`dsiq_fifo`) and its status byte |
| **CORE** | `…/gateware/Original/gateware/rtl/hermeslite_core.v` | Parameters (NR, version) and wiring |
| **AD** | `…/gateware/Original/gateware/rtl/ad9866.v`, `ad9866ctrl.v` | LNA gain and TX drive decoding |

The user's fork `Hermes-Lite2` (gateware 74.102, `*.sv`
files) has the same protocol logic. The only protocol-visible difference is in the
extended-debug discovery bytes: there, the reported defaults for TX latency and PTT
hang are 20/12 ms instead of 10/4 ms, so they match `radio.v`.

Notation: `DATA[31:0]` is the 32-bit word `C1:C2:C3:C4`, with C1 as the MSB
(`DATA[31:24]=C1`, `[23:16]=C2`, `[15:8]=C3`, `[7:0]=C4`). `ADDR` is the 6-bit
register address. The C0 byte on the wire is `C0 = RQST<<7 | ADDR<<1 | MOX`, so
ADDR 0x0a appears as C0 = 0x14 or 0x15. All multi-byte fields are big-endian.

---

## 1. Transport

* The radio listens on UDP port **1024**. RTL: `eth_port[15:1]==512` accepts 1024 and 1025 (DS:185). Port **1025** is an HL2-only
  "alternate" control channel, covered in §9. The radio always transmits **from** port 1024 (or from 1025 when replying to 1025).
* All packets start with the magic `0xEF 0xFE` followed by a type byte (DS:192-198):

| Byte 2 | Meaning | Direction |
|---|---|---|
| `0x01` | Data frame. Byte 3 is the endpoint: `0x02`=EP2 (host→radio), `0x06`=EP6 (radio→host IQ), `0x04`=EP4 (wideband) | both |
| `0x02` | Discovery request. In a reply, `0x02`/`0x03` is the status byte | both |
| `0x03` | Flash programming (ASMI), or in a reply "erase done" | – |
| `0x04` | Start/Stop | host→radio |
| `0x05` | Command-only packet, accepted on port 1025 unicast only (§9) | host→radio |

* **Where the radio streams to.** The radio latches the destination IP, MAC and UDP port from
  every port-1024 packet it receives *while not running*. Once `run=1`, the destination is frozen
  (`ethernet/network.v:686-697`). In practice the stream goes to the source address/port of the
  Start packet. An emulator should do the same and send EP6 to the `(ip, port)` that sent Start.
  METIS says the same: data goes to the "from port" of the PC.
* If the radio receives ICMP "destination unreachable", it drops `run` (DS:128-132). METIS: "Metis will stop sending if it receives a Destination Unreachable".

---

## 2. Discovery

### 2.1 Request (host → radio, broadcast or unicast, port 1024)

```
EF FE 02 + 60 × 00      (63 bytes per METIS)
```
The RTL only looks at the first 3 bytes (DS:206-209); length and padding are ignored.
Unicast works too, because there is no broadcast check on type 0x02. The radio also
answers discovery **while it is running**, with status 0x03.

### 2.2 Reply (radio → requester's IP:port, from port 1024), 60 bytes

Built in US:250-320 (`udp_tx_length = 0x3c`, US:235). The byte offset is `0x3b - dbyte_no`.

| Off | Content | Source / notes |
|---|---|---|
| 0x00 | `0xEF` | |
| 0x01 | `0xFE` | |
| 0x02 | Status: `0x02` idle, `0x03` streaming (`run`) | US:266 |
| 0x03-0x08 | MAC U:V:W:X:Y:Z. The default HL2 MAC is `00:1C:C0:A2:xx:xx`, with the last 2 bytes from EEPROM if valid | US:267-272; CORE:104 |
| 0x09 | Gateware **major** version (currently `74` decimal = 0x4A; `54` on beta2) | US:273; CORE:142 |
| 0x0A | **Board ID**: `0x06` = HL2 (`0x01` if the "Hermes emulation" jumper is set) | US:274 |
| 0x0B | EEPROM config bits `[7:5]` (b7 valid IP, b6 valid MAC, b5 favour DHCP). The rest is 0 | US:276; WIKI "Configuration EEPROM" |
| 0x0C | 0x00 (EEPROM reserved) | |
| 0x0D-0x10 | Fixed IP W.X.Y.Z from EEPROM | US:278-281 |
| 0x11-0x12 | Alternate MAC bytes Y,Z from EEPROM | US:282-283 |
| 0x13 | **Number of hardware receivers (NR)**. Standard build is 4 (variants have 2, 5, 6 or 10). In HL2-link master mode it is doubled (`{NR,0}`) | US:284; variants `hl2b5up_main NR=4` |
| 0x14 | `[7:6]` wideband sample format (`01` = 16-bit two's complement), `[5:0]` board build (5 = hl2b5up, 3 = b3/b4, 2 = beta2) | US:285; BANDSCOPE_BITS=2'b01 |
| 0x15 | Gateware **minor** version / patch (upstream `2`, fork `102`) | US:286 |
| 0x16 | 0x00 reserved | |
| 0x17-0x1A | Last port-1025 response data [31:0] | US:288-291 |
| 0x1B | `resp_control = {ext_cwkey, ptt_resp, pa_exttr, pa_inttr, tx_on, cw_on, clip_cnt[1:0]}`. The wiki documents only b7=CW key, b6=PTT, b1:0=clip | US:292; CTL:899 |
| 0x1C-0x1D | Temperature (12-bit, MSB nibble first) | US:293-294 |
| 0x1E-0x1F | Forward power (12-bit) | |
| 0x20-0x21 | Reverse power (12-bit) | |
| 0x22-0x23 | Bias current (12-bit) | |
| 0x24 | TX IQ FIFO status: `[7]` under/overflow recovery flag, `[6:0]` FIFO count MSBs (see §7) | US:301 |
| 0x25 | EP2 packet counter (8-bit, wraps). Only when `EXTENDED_DEBUG_RESP=1`, which the main variant has | US:302,522 |
| 0x26 | TX buffer latency in ms (the value last written to reg 0x17) | US:303 |
| 0x27 | CW hang time `[7:0]` | US:304 |
| 0x28 | `{cw_hang[9:8], 0, ptt_hang[4:0]}` | US:305 |
| 0x29 | `{sample_rate[1:0], cmd_ptt(MOX), tx_wait, receivers[3:0]}` | US:306 |
| 0x2A-0x3B | 0x00 | |

For a minimal emulator, fill 0x00-0x0A, 0x13, 0x14 and 0x15 and leave the rest zero.
Choose the version bytes carefully: clients gate features on them (see
`client-behavior.md`). Report major ≥ 73 and minor ≥ 1 to look like a current HL2.

The standard Metis layout is `<EFFE><status><MAC 6><ver><board_id><49×0x00>`, 60 bytes (METIS p.4).
HL2 fills some of the zero bytes, so clients that don't know about HL2 still parse it.

---

## 3. Start / Stop

```
EF FE 04 <cmd> + 60 × 00
cmd bit0 = 1: start IQ+mic stream (EP6)     0: stop
cmd bit1 = 1: start wideband (EP4)           0: stop
cmd bit7 = 1: DISABLE watchdog (HL2 only; for RX-only apps such as CW Skimmer)
```
DS:200-204 and DS:399-401. Watchdog disable is taken from `eth_data[7]` of the command byte.
Typical values are `0x01` (start IQ), `0x03` (IQ + wideband) and `0x00` (stop).

* When `run` is cleared, the EP6 and EP4 sequence numbers reset to 0 (US:176-178). The host is
  expected to reset its EP2 sequence number on each Start (METIS). **HL2 never checks the EP2
  sequence number**: the SEQNO states just skip 4 bytes (DS:258-276).
* Stop/Start while TX is on: `tx_state` is forced to `NOTX` when `run=0` (RADIO: `tx_state <= run ? … : NOTX`).
* Some clients (Thetis, piHPSDR) send one or two EP2 packets *before* Start to preload
  C&C. HL2 accepts them: EP2 is parsed whether or not `run` is set.

---

## 4. Data packet (both directions): 1032 bytes

```
off 0   EF FE 01 <EP>                       EP = 0x02 host→radio, 0x06 radio→host, 0x04 wideband
off 4   SEQ[31:24] SEQ[23:16] SEQ[15:8] SEQ[7:0]   big-endian
off 8   USB frame #1 (512 bytes)
off 520 USB frame #2 (512 bytes)
```
Each USB frame is `7F 7F 7F C0 C1 C2 C3 C4` followed by 504 payload bytes (USB p. "Protocol").

### 4.1 Sequence numbers

* **EP6 from HL2:** byte 4 is always 0x00. Only a 20-bit counter is carried (`{4'h0,seq[19:16]}`,
  US:383-388), so the HL2 EP6 sequence wraps at 2^20. Metis specifies 32 bits. An emulator
  should use a full 32-bit counter, which is safe for all clients. It increments per EP6 packet
  and resets to 0 on Stop.
* EP4 uses its own counter. HL2 keeps its low 2 bits aligned to 4-packet (2048-sample) blocks (US:182).
* **EP2 from the host:** ignored by the radio (DS:258-268).

---

## 5. EP2: host → radio (C&C + L/R audio + TX IQ)

### 5.1 Frame layout (per 512-byte frame, 63 samples)

```
0   7F 7F 7F C0 C1 C2 C3 C4
8   L1 L0 R1 R0 I1 I0 Q1 Q0     sample 0      (8 bytes/sample, 16-bit signed BE)
16  L1 L0 R1 R0 I1 I0 Q1 Q0     sample 1
…                               63 samples × 8 = 504 bytes
```
USB lines 499-527. So each 1032-byte EP2 packet carries **126 TX IQ samples**, two
C&C words, and 126 L/R audio samples. The TX IQ and L/R rate is **always 48 kHz**,
independent of the RX sample rate (USB line 495). This means that during TX the host
must deliver 48000/126 ≈ **380.95 EP2 packets/s**, one every 2.625 ms.

RTL parsing details:
* The sync check only requires the 3rd sync byte to satisfy `[7:2]==0x1F` (DS:278-292), and
  `[1:0]` of that byte is latched as `ds_cmd_mask` (used only by HL2-link). Always send `7F 7F 7F`.
* **TX IQ is pushed into the TX FIFO only when C0.MOX=1 in that same frame** (or in CWX mode).
  `dsethiq_tvalid = ds_cmd_ptt | cwx_pushiq` (DS:350-371). IQ in frames with MOX=0 is discarded.
  MOX is sampled per 512-byte frame, not per packet.
* L/R audio bytes go to `dsethlr` and are unused on a standard HL2. The AK4951 variant plays them
  on its codec; the LRDATA=1 variant uses them for predistortion tables. The wiki says the first
  L/R word after C4 is "reserved: [15:0] EADDR for extended writes to 0x3f" (WIKI:139-146), but
  nothing implements it. Clients send RX audio or zeros here. An emulator can ignore L/R.
* TX I/Q are 16-bit signed big-endian. The TX-path I/Q swap convention is historical: "The I&Q
  samples, relative to receive, are reversed" (USB line 532). Clients already compensate, and the
  HL2 plays I as `tx_tdata[31:16]` (first 16-bit word) and Q as the second (RADIO:1039-1040).
  An emulator feeding a real transmitter must decide the sideband empirically. Expected
  convention: the first word is the real/in-phase component as the client intends it after its
  own swap. Verify with a USB-mode tone (positive audio tone → RF above the carrier).
* TX amplitude is full-scale ±32767 (clients normalise to ±1.0). **Output power is set by the drive level (ADDR 0x09), not by IQ amplitude** on Hermes-class radios (USB line 530). HL2 uses only drive `[31:28]` (4 bits, AD ctrl:134-141).

### 5.2 C0 byte (host → radio)

| Bit | Meaning |
|---|---|
| 7 | **RQST**: HL2 extension. Ask for an ACK response (§6.3). Standard openHPSDR used bits 7:1 as the address, but no standard address ≥ 0x40 is used |
| 6:1 | ADDR[5:0] |
| 0 | **MOX** (1 = transmit). Must be set in *every* frame while transmitting (DS:294-299) |

Clients send one address per frame and cycle through their list round-robin, so each
packet carries two addresses. HL2 processes every frame's C&C immediately (DS:317-326).
The radio keeps state per register, so the order does not matter and a register only
needs to be re-sent when it changes. The watchdog does, however, require packets to
keep arriving (§8).

### 5.3 Register map (host → radio)

"C0" in the table below is the wire value with MOX=0 (add 1 for MOX=1). Bits refer to `DATA[31:0]`.

| ADDR | C0 | DATA bits | Meaning on HL2 | Std openHPSDR meaning | RTL |
|---|---|---|---|---|---|
| 0x00 | 0x00 | [25:24] (C1[1:0]) | **RX sample rate**: 00=48k, 01=96k, 10=192k, 11=384k | same | RADIO:269-274 |
| | | [23:17] (C2[7:1]) | Open-collector outputs → I²C 0x20 byte bits 6:0 (N2ADR filter board, one-hot LPF/HPF) | Penelope/Hermes OC | i2c_bus2.v:196-205 |
| | | [16] (C2[0]) | pa_mode (EER/class-E) | Class E | RADIO:271 |
| | | [13] (C3[5]) | "Rx antenna" → I²C 0x20 byte bit 7 | Alex Rx ant | i2c_bus2.v:201 |
| | | [12] (C3[4]) | Disable FPGA PSU switching clock | Alex Rx ant | WIKI:38 |
| | | [11] (C3[3]) | Fan (0) / Band-volts PWM (1) | LT2208 random | WIKI:39 |
| | | [10] (C3[2]) | VNA fixed RX gain (0=-6 dB, 1=+6 dB) | dither | WIKI:40 |
| | | [6:3] (C4[6:3]) | **Number of receivers − 1** (0..11) | C4[5:3] = nrx−1 (1..8) | RADIO:272; US:137,506 |
| | | [2] (C4[2]) | Duplex (clients set 1). With 0, RX1 follows the TX frequency | duplex | RADIO:273 |
| 0x01 | 0x02 | [31:0] | **TX NCO frequency, Hz** | TX freq | RADIO:254 |
| 0x02 | 0x04 | [31:0] | **RX1 NCO frequency, Hz** | RX1 | RADIO:255 |
| 0x03-0x08 | 0x06-0x10 | [31:0] | RX2..RX7 frequency, Hz | RX2..RX7 | RADIO:256-261 |
| 0x09 | 0x12 | [31:28] (C1[7:4]) | **TX drive level** (C1 0..255, only the top nibble is used → 16 steps of AD9866 TX gain) | Drive 0-255 | AD ctrl:134-141 |
| | | [23] (C2[7]) | VNA mode | VNA | CTL:212 |
| | | [22] (C2[6]) | Alex manual filter mode (not implemented) | Alex manual | WIKI:53 |
| | | [20] (C2[4]) | Tune request (external ATU) | Apollo auto-tune | exttuner.v:68 |
| | | [19] (C2[3]) | **Onboard PA enable** (1=on) | Apollo tuner | CTL:213 |
| | | [18] (C2[2]) | Disable T/R relay when PA off (antenna stays on RX) | Apollo filter | CTL:214 |
| | | [17] (C2[1]) | Tune: send ATU bypass | line-in | exttuner.v:69 |
| | | [15:0] | Alex HPF/LPF bytes, or VNA count | Alex filters | RADIO:276-279 |
| 0x0a | 0x14 | [22] (C2[6]) | PureSignal enable | PS | RADIO:280 |
| | | [6] (C4[6]) | **1 = full-range LNA mode** | Hermes att enable (bit5) | AD:133-135 |
| | | [5:0] (C4[5:0]) | **LNA gain**. Mode bit6=1: gain dB = value − 12, value 0..60 → **−12..+48 dB**. Legacy (bit6=0): if bit5=1, value[4:0] is attenuation 0..31 dB from +19 dB (reg = `~value`); if bit5=0, fixed +20 dB (reg = `{1,value[4:0]}`) | Hermes 0-31 dB attenuator | AD:134; ad9866ctrl.v:147-160 |
| 0x0b | 0x16 | [22] reverse paddles, [15:14] mode (0 straight, 1 iambic A, 2 iambic B), [13:8] WPM, [7] spacing, [6:0] weight | Internal iambic keyer (only in `CW=2` builds, not the standard CW=1 build) | same | cw_openhpsdr.v:29-34 |
| 0x0e | 0x1C | [15] enable separate TX LNA gain, [14] mode, [13:8] TX LNA gain | Only in FAST_LNA builds (all current variants) | ADC assign / TX att | AD:137-139 |
| 0x0f | 0x1E | [24] (C1[0]) | **Enable CWX**: CW key is carried in the TX I-sample LSBs (§5.4) | CW internal/external | DS:393-394 |
| | | [15:8] (C3) | CW PTT delay, ms (iambic build) | CW PTT delay | cw_openhpsdr.v:36 |
| 0x10 | 0x20 | [31:24],[17:16] | CW hang time, ms (10 bits) | same | RADIO:952-953 |
| 0x11 | 0x22 | PWM min/max (EER). In the iambic build, mode-B memory timing | same | RADIO:282-286 |
| 0x12-0x16 | 0x24-0x2C | [31:0] | RX8..RX12 frequency, Hz | 0x12 = 2nd Alex filters (Thetis also sets PS bit here) | RADIO:262-266 |
| 0x17 | 0x2E | [12:8] (C3[4:0]) | **PTT hang time** ms. Default 12. 31 (saturated) = exit TX immediately on MOX=0 | HL2 only | RADIO:954-956,1033 |
| | | [6:0] (C4[6:0]) | **TX buffer latency** ms. Default 20 (RADIO:934); wiki says 20, upstream US debug copy says 10 | HL2 only | RADIO:954-956 |
| 0x2b | 0x56 | [31:24] subindex, [19:16] value | Predistortion | – | RADIO:288-292 |
| 0x39 | 0x72 | [27:24] | Misc: `0x8` enable / `0x9` disable watchdog. `0xB`/`0xA` enable/disable "force discover" (a status reply to port 1025 after every EP6; undocumented) | HL2 only | DS:395-397; US:512-514 |
| | | [23],[21:16] | Receiver lock/sync | | RADIO:957 |
| | | [11:8],[7:4],[3:0] | Master (HL2-link), NCO/filter sync, clock-generator commands | | i2c.v:171-180 |
| 0x3a | 0x74 | [0] | Reboot FPGA from flash on disconnect (applied when `run=0`) | HL2 only | CTL:216-222 |
| 0x3b | 0x76 | [31:24]=0x06 cookie, [20:16] addr, [7:0] data | Raw AD9866 SPI write | HL2 only | ad9866ctrl.v:164-170 |
| 0x3c | 0x78 | [31:24] cookie 0x06 write / 0x07 read, [23] stop, [22:16] I²C addr, [15:8] reg, [7:0] data | I²C bus 1 (Versa clock 0x6a, AK4951 0x12) | HL2 only | WIKI:108-112 |
| 0x3d | 0x7A | same | I²C bus 2: MCP4662 bias/EEPROM (0x2c), N2ADR filter (0x20), IO board (0x1d) | HL2 only | WIKI:113-117 |
| 0x3f | 0x7E | – | Error code in responses | | |

Notes:
* RX sample-rate selection: the host writes ADDR 0x00 C1[1:0]. The EP6 packet rate changes
  immediately. The TX IQ rate stays 48 kHz.
* Receiver count: HL2 reports NR in discovery byte 0x13. A client asking for more receivers than
  NR is not guarded by the RTL; the output would contain garbage or zeros. An emulator should
  advertise the NR it wants to support, possibly 1 or 2, and handle `nrx−1` up to that.
* Frequencies are plain Hz. Thetis has a "phase word" mode for some boards, but for HL2 it sends Hz.
* I²C writes to 0x3c/0x3d with cookie 0x06 execute even without RQST. An emulator should accept
  them and ignore them. It may emulate the N2ADR filter byte (0x3d, addr 0x20) or IO-board
  registers if useful (see `ref/HL2IOBoard-i2c_registers.h`).

### 5.4 CW on HL2

HL2 CW works in three ways.

1. **External key on the HL2 jack (CN4)**: tip = key, ring = PTT. HL2 generates the shaped CW envelope itself at the
   TX NCO frequency and reports `ptt_resp`/dot in EP6 C0 (§6.2). **The host must send MOX=0 during CW**
   (WIKI:199-202), and TX IQ is not used. The default "CW_BASIC" build has no iambic keyer: the
   tip is a straight key and the ring is PTT (CTL:790-795). `CW=2` builds add the iambic keyer
   configured by 0x0b (CTL:797-822).
2. **CWX (host-keyed, HL2-shaped)**: enable with 0x0f DATA[24]=1. The host keeps **MOX=0**
   and streams EP2 packets. In each TX sample, the **LSB byte of I (I0)** carries
   `bit0 = key down` and `bit3 = CWX PTT` (called `cwx_keyup` in RTL; it holds TX in the CW state
   between elements). Thetis HL2 actually writes `cwx_ptt<<3 | dot<<2 | dash<<1 | cwx` into
   *every* 16-bit I and Q word when CWX is on (Thetis `networkproto1.c`:1252-1257). DS:360
   saves them, and they reach the FIFO attached to the *next* sample (1-sample delay). RADIO:974-976
   decodes `cwx_keyup=tuser[1]`, `cwx_keydown=tuser[2]`. After a key bit has been seen, samples keep
   being pushed for 500 ms (DS:419-424). The shaped envelope is generated in the FPGA and delayed by
   `tx_buffer_latency`, so CWX timing is jitter-free as long as packets are not starved.
3. **Host-generated CW as ordinary IQ with MOX=1**: piHPSDR/Thetis do this when the internal
   keyer is off. The radio treats it like SSB.

For the QMX emulator, the most practical mapping is: (3) forward the IQ tone as audio.
Better, detect CWX key bits (2) and key the QMX directly by CAT or its paddle input. For
(1), the QMX's own key jack does the job.

---

## 6. EP6: radio → host (C&C status + RX IQ + mic)

### 6.1 Frame layout

```
0   7F 7F 7F C0 C1 C2 C3 C4
8   [ I1_2 I1_1 I1_0  Q1_2 Q1_1 Q1_0 ] … [ In_2 In_1 In_0  Qn_2 Qn_1 Qn_0 ]  M1 M0     ← one "round" = 6n+2 bytes
…   repeated floor(504/(6n+2)) times, remainder padded with 0x00
```
US:396-491. The RTL emits I,Q (24-bit signed, big-endian) for each receiver, then a 16-bit mic word,
and pads with zeros when the next round won't fit (`byte_no > round_bytes` test, US:478).

| nrx | bytes/round | rounds (samples per rx) per frame | per packet | pad bytes/frame |
|---|---|---|---|---|
| 1 | 8 | 63 | 126 | 0 |
| 2 | 14 | 36 | 72 | 0 |
| 3 | 20 | 25 | 50 | 4 |
| 4 | 26 | 19 | 38 | 10 |
| 5 | 32 | 15 | 30 | 24 |
| 6 | 38 | 13 | 26 | 10 |
| 7 | 44 | 11 | 22 | 20 |
| 8 | 50 | 10 | 20 | 4 |
| 9..12 | 56/62/68/74 | 9/8/7/6 | 18/16/14/12 | 0/8/28/60 |

(USB line 383-386 gives the padding table for 1-8 receivers.)

* **Mic word**: on a standard HL2 this is always 0x0000 (there is no codec). The exception is VNA mode,
  where `M0[0]` carries a VNA flag (US:124-127). AK4951 builds send real mic samples. Clients use
  the mic stream as TX audio input when "radio mic" is selected. An emulator can send zeros,
  or QMX audio if desired.
* **EP6 rate**: the HL2 sends an EP6 packet whenever ≥334 24-bit words are queued (US:238). So the
  packet rate is exactly `fs / samples_per_packet_per_rx`, e.g. 48k/1rx = **380.95 pkt/s**,
  384k/1rx = 3047.6 pkt/s, 192k/2rx = 2666.7 pkt/s. Packets arrive in near-uniform bursts. There is no
  other pacing, and the radio's ADC clock is the master clock of the whole system.
* The I/Q sign convention on receive is standard (I=real). The TX-side I/Q swap mentioned in §5.1 applies to TX only.

### 6.2 C0 status byte (ACK=0, "classic" rotation)

```
C0[7]   ACK = 0
C0[6:3] RADDR[3:0]  (HL2 uses 0..3 → C0 = 0x00/0x08/0x10/0x18 | low bits)
C0[2]   DOT  = external CW key (tip) state
C0[1]   DASH = 0 always (CW_BASIC build)
C0[0]   PTT  = cw_on | ext_ptt   (HL2-generated CW TX active or ring-PTT; NOT an echo of MOX)
```
CTL:456, 472-475. WIKI:185-202. The standard layout used C0[7:3] as the address, and HL2 is compatible because RADDR ≤ 3.

The rotation advances by one address per EP6 *frame*, so every packet carries two
consecutive addresses. When an ACK is pending it replaces the slot (CTL:462-481):

| C0 (ACK=0) | C1 | C2 | C3 | C4 |
|---|---|---|---|---|
| 0x00 | `0b000111 , ~txinhibit , adc_overload` (bit0 = **ADC overload** = ≥3 clips since the last frame; bit1 = 1 when TX *not* inhibited; bits 4:2 = 1) | 0x00 | **TX FIFO status**: `[7]` under/overflow recovery, `[6:0]` FIFO count MSBs (§7) | **Gateware major version** (e.g. 74) |
| 0x08 | Temperature [11:8] | Temperature [7:0] | **Forward power** [11:8] | Forward power [7:0] |
| 0x10 | **Reverse power** [11:8] | Reverse [7:0] | Bias current [11:8] | Current [7:0] |
| 0x18 | 0 | 0 | debug (0) | debug (0) |

Compared with the standard (USB lines 394-445): addr 0 has C2=Mercury version, C3=Penny version,
C4=Metis/Hermes version. C4 matches, and HL2 reuses C3 for the FIFO status. Addr 1 has
C1:C2=AIN5 "exciter/Penelope forward power" (HL2 puts temperature there) and C3:C4=AIN1 "Alex
forward power". Addr 2 has AIN2 reverse and AIN3. So an unmodified Hermes client shows the HL2's
temperature as "exciter power". HL2-aware clients know the difference.

Conversion formulas (as used by clients; see `client-behavior.md`):
* Temperature °C = (3.26 × (raw/4096) − 0.5) / 0.01 (TMP36 on the 3.26 V ADC). An emulator should report ~25-35 °C.
* Current mA = ((3.26 × raw/4096)/50)/0.04 × 1000 × (1270/1000)… The exact formula is client-specific. Report a plausible small value.
* Forward/reverse power: 12-bit raw counts. Clients apply a per-radio calibration (piHPSDR HL2:
  `v = raw/4095 × 3.3 ; W = v²/1.5`-style). An emulator can back-calculate from QMX
  power readings or send 0.

### 6.3 ACK responses (C0[7]=1)

If the host set RQST in C0, the radio answers once, in a later frame, with
```
C0 = 1<<7 | ADDR<<1 | PTT       C1..C4 = echoed DATA (writes) or 4 bytes of I²C read data
```
* If I²C or the AD9866 was busy, the reply has ADDR=**0x3f** (error) with the original data (CTL:412-420).
* ACKs are only inserted on every other `resp_rqst` (CTL:437, 468). At most one request may be outstanding (WIKI:247).
  An I²C read takes < 5 ms.
* An emulator that supports I²C reads (e.g. Thetis reading the HL2 EEPROM or IO board) must
  implement this. Otherwise, echo writes with ACK when RQST is set, and answer reads with plausible
  data or the 0x3f error.

---

## 7. TX path timing: FIFO, latency, PTT hang (critical for the emulator)

HL2 gateware behaviour (RADIO:997-1050, FIFO:30-116):

* **TX IQ FIFO**: 16384 bytes = **4096 IQ samples ≈ 85.3 ms @ 48 kHz** (`DSIQ_FIFO_DEPTH=16384`, CORE:136).
  On overflow, writes are dropped until the fill level falls to ≤ 4096 bytes (1024 samples, ~21 ms) (FIFO:59-67).
* **Status byte** (EP6 addr 0 C3, and discovery byte 0x24): `{recovery_flag, rd_count[6:0]}`, where
  `rd_count = FIFO_samples >> 5`. **One count = 32 samples = 0.667 ms**; the maximum of 127 is about 84.7 ms.
  It is updated once per 4 EP6 frames (sampled at resp_addr==1, CTL:486-489). The recovery flag
  is set if the FIFO ran empty or was dropping writes since the last sample. The wiki reads
  `[15:14]==10` as underflow and `11` as overflow, i.e. the flag plus the MSB of the count.
* **TX start (MOX 0→1)**: the TX state machine leaves NOTX when a FIFO sample tagged with MOX
  reaches the head (`ds_cmd_ptt & ptt`). It then enters **PRETX** and **holds the FIFO for
  `tx_buffer_latency` ms (default 20 ms)** to pre-fill. After that it goes to **PTTTX** and plays
  one sample per 48 kHz tick. RF therefore starts about latency ms after the first MOX frame arrives.
* **During TX**, each sample popped must carry the MOX tag. If the FIFO runs empty (host
  starvation), `ptt` goes false and **after `ptt_hang_time` (default 12 ms) without samples, TX
  drops to NOTX** (RADIO:1030-1050). TX resumes with a new PRETX latency when samples come back.
  So HL2 tolerates up to about 20 ms (latency) + 12 ms (hang) of jitter before dropping TX.
* **TX end (MOX 1→0)**: frames with MOX=0 stop pushing IQ. The buffered samples are played out,
  then the hang timer expires. Result: TX ends about latency + hang ms after the last MOX frame.
  If `ptt_hang_time=31`, TX ends immediately when MOX=0 is seen, without draining.
* **Client adaptation**: the TX FIFO count in EP6 lets clients measure how full the radio's
  buffer is. Most clients do *not* close a loop on it. They rely on sending EP2 at the same
  rate EP6 arrives (see `client-behavior.md`). Because the HL2's 48 kHz TX clock and its RX
  clock come from the same oscillator, pacing EP2 from EP6 is exactly rate-locked.

**Emulator implications**
1. The EP6 stream the emulator generates *is the master clock* for the client's TX IQ. If EP6 is
   paced by the QMX's USB-audio RX clock, the client's EP2 TX IQ arrives locked to that
   clock at 48 kHz × (126 per packet). Feeding that into the QMX's USB-audio TX (another clock
   domain, same device) still needs a small adaptive resampler or a drift-tolerant buffer.
2. Report a sane TX FIFO count (e.g. the fill of the emulator's own TX jitter buffer in 32-sample units)
   and keep the recovery bit 0 unless a real underrun happens. Some clients display it, and
   Thetis/piHPSDR may log underflows.
3. Implement "RF follows MOX-tagged samples plus latency" semantics: switch the QMX to TX only
   when MOX frames arrive (optionally after a pre-fill), and release after the IQ drains plus a hang time.

---

## 8. Watchdog and timeouts

* A 12-bit counter is incremented by `watchdog_up` ticks and cleared by **every EP2 data packet**
  (at SEQNO0, DS:270-276), by `run=0`, or when the watchdog is disabled (DS:406-414). At 4095 ticks the radio
  sets `run=0` and MOX=0, and stops streaming (DS:128-132).
* `watchdog_up` toggles once every `(C4[7:3]+1) << speed` EP6 packets (US:137, 242-244). That works out
  to about 316-381 ticks/s whatever the rate, so the **timeout is about 11-13 s** with no EP2 packets.
  (This is derived from the RTL, not documented.)
* Disable it with Start cmd bit 7 or with reg 0x39 `[27:24]=0x9`.
* An emulator should implement a similar timeout: stop streaming and drop PTT if no EP2 arrives for
  several seconds. Also treat `MOX` stuck at 1 with no EP2 as a fault and drop TX much sooner
  (e.g. 100-200 ms), to protect the QMX PA. The real HL2 achieves this through FIFO underflow plus PTT hang.

---

## 9. Port 1025 "alternate" command channel (HL2 only; optional)

* Packet: `EF FE 05 7F C0 C1 C2 C3 C4` sent unicast to port 1025 (DS:197, 288-326; the byte after 05 must satisfy `[7:2]==0x1f`).
  There is no IQ. It executes a register write or read like EP2 C&C.
* The response comes back as a **60-byte discovery-format packet from port 1025**, with response
  data in bytes 0x17-0x1A and status fields 0x1B-0x29 (US:146-147, 288-306).
* Discovery (`EF FE 02`) to 1025 also works. It is used by `hl2setup`/`hermeslite.py`-style tools to
  change settings while another program streams. SDR clients don't need it. An emulator can skip it.

---

## 10. Wideband (EP4)

Start bit1 enables it. Packets are `EF FE 01 04 + seq + 512 × 16-bit samples` (big-endian, 12-bit
left-justified in the older format). It carries 2048 consecutive raw ADC samples @ 76.8 MHz per
4 packets (seq low 2 bits == 0 marks the block start) (WIKI:204-208; US:323-367). A QMX emulator
cannot provide this. It should never send EP4, even if requested. Clients handle the absence
gracefully (the wideband display just stays empty), though this should be verified per client.

---

## 11. Checklist: fields clients actually rely on

(Details and citations are in `client-behavior.md`.)

| Field | Needed? | Notes |
|---|---|---|
| Discovery reply bytes 0-2, 3-8 (MAC), 9 (version), 10 (board id = 6) | **must** | Board ID 6 selects HL2 behaviour in every client |
| Discovery 0x13 (NR), 0x14, 0x15 (minor) | should | piHPSDR needs `10*byte9 + byte0x15 >= 400` to pick HL2 (not HL1); Quisk reads NR from 0x13 |
| Status byte 0x02/0x03 | must | 0x03 makes some clients show "in use" and refuse to connect, so reply 0x02 when idle |
| EP6 header, 32-bit seq, 2 frames, sync, correct round layout for nrx | **must** | |
| EP6 C0 PTT/dot bits | should | Report 0 unless the QMX's own PTT/key is pressed; if so, report PTT so the client follows |
| EP6 addr 0 C4 (version) | should | Some clients read the firmware version here |
| EP6 addr 0 C1 bit0 (overload) | nice | Map to QMX clipping if detectable |
| EP6 addr 0 C3 (TX FIFO) | nice/should | Keep it plausible (e.g. 10-30 counts during TX), recovery bit 0 |
| EP6 addr 1/2 power/temp/current | nice | Map QMX SWR/power if available |
| ACK (C0[7]) responses | **must** | Quisk queues 0x17/0x39/0x3a writes with RQST and waits for, or retries on, the ACK. pi/desk/Thetis use it for IO-board and I²C reads |
| EP6 addr 0 C1 bit1 (=1, TX *not* inhibited) | **must** | Quisk derives `tx_inhibit = !(C1&2)`. Send C1 = 0x1E like the gateware |
| NR ≥ 4 and all 4 DDCs filled | **must** for Thetis | Thetis HL2 always runs nddc=4; piHPSDR uses 4 with PureSignal |
| Host→radio: 0x00 rate/nrx/duplex, 0x01 TX freq, 0x02 RX1 freq, 0x09 drive/PA, 0x0a LNA, MOX bit | **must** | Core to map onto QMX CAT |
| 0x17 latency/hang, 0x0f/0x10 CW, 0x0b keyer | should | TX/CW semantics |
| 0x39-0x3d | accept and ignore (ACK if RQST) | |
