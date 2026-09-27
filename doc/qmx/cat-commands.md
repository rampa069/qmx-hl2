# QMX CAT command reference

Primary source: **QMX CAT programming manual rev 1_04_004** (23-Jul-2026), `ref/cat_1_04_004.pdf`
(text: `ref/cat_1_04_004.txt`). "Since" column from the firmware change log on qmx.html.
Items marked [field] come from third-party code; [UNVERIFIED] = not confirmed.

## General rules

- ASCII, every command terminated by `;`, **never CR/LF**. Multiple commands may be concatenated.
- **A CR (0x0D) switches that serial port into terminal (menu application) mode.** Never send CR.
  On Linux stop ModemManager probing the port.
- Get = command with no parameters (`FA;`), Set = with parameters (`FA7074000;`).
- Error reply: `?;`. Well-formed setters return nothing [field SDR++-iak]; one project reports
  asynchronous echoes of set commands (e.g. `Q91;`) [field SteffenLav, UNVERIFIED].
- Two-letter commands affect the current session only (not EEPROM). `Qx` commands likewise.
  `MM` Set commands **are** stored in EEPROM.
- Up to 3 USB CDC ports (+ AUX/PTT UARTs); any port can do CAT; only one host app per port.
- Speed: CAT round-trip < 50 ms [field pd4hs]; 50-100 ms polling works in practice [field
  SteffenLav 50 ms, SDR++-iak IF; every 100 ms]. The manual suggests a 10 s poll for WSJT-X
  only out of caution. AI2 (event-driven IF push) available since 1_04_000 but one project
  saw AI1 corrupt a 1_03 session [field, UNVERIFIED on 1_04].
- CAT timeout (QB/QC): if enabled and TX is active, no CAT traffic for N s -> back to RX.
  An emulator holding TX must keep sending something (e.g. `TQ;`) or disable it.

## Command table (alphabetical)

| Cmd | Get | Set | Reply / params | Since | Notes |
|---|---|---|---|---|---|
| AG | `AG;` or `AG0;` | `AG0nnn;` | `AG0nnn;` AF gain (volume) in 0.25 dB steps, 000-799 | 1_02_006 (was RF gain before) | |
| AI | `AI;` | `AIn;` | 0 off; 1 old: IF; on change + every 1.5 s; 2 extended: IF; on change; 3 both | 1_04_000 | unsolicited IF; frames |
| BD | – | `BD;` | band down | 1_04_003 | |
| BN | `BN;` | `BNn;` | band = column index of Band config table (e.g. 80-20m: 0=80..4=20; QMX+ 0=160) | 1_04_003 | `?;` if not configured |
| BU | – | `BU;` | band up | 1_04_003 | |
| C2 | `C2;` | `C2<Hz>;` | Si5351 Clk2 signal generator frequency | early | test use |
| FA | `FA;` | `FA<Hz>;` | reply 11 digits `FA00007030000;` | early | VFO A |
| FB | `FB;` | `FB<Hz>;` | 11 digits (manual example shows `FA` prefix in reply — typo?) | early | VFO B |
| FR | `FR;` | `FRn;` | Set: 0=VFO A,1=VFO B,2=Split. Get: 0 = A used for RX, 1 = B | early | not exactly TS-480 |
| FT | `FT;` | `FTn;` | Set as FR. Get: 0 = A used for TX, 1 = B used for TX | early | |
| FW | `FW;` | – | 3200 in Digi, 0300 in CW (actual CW BW since 1_02_001) | early | read-only |
| GP | `GP;` | – | `GP+DD.DDDDDD+DDD.DDDDDD+YYYYMMDDHHMMSS;` | 1_04_004 | lon sign fixed 1_04_005 |
| ID | `ID;` | – | always `ID020;` (TS-480) | early | |
| IF | `IF;` | – | 11-digit freq, 5 spaces, RIT ±9999 (5 chars), RIT on, XIT 0, mem bank 0, mem ch 00, TX (0/1), mode char (MD code), RX VFO (0/1), scan 0, split (0/1), tone 0, tone no 0, space | early | TS-480 layout |
| KD | `KD;` | `KDn;` | 1 key down (TX), 0 key up | 1_04_000 | CW mode only (`?;` in DiGi [field]) |
| KS | `KS;` | `KSnn;` | keyer WPM | 1_00_021 | |
| KY | `KY;` | `KY <text>;` | non-compat mode: `KY0;` sending & buffer ≤75 %, `KY1;` >75 %, `KY2;` idle, `?;` overflow; 80-char circular buffer. TS-480 compat mode (menu): fixed 24-char field, `KY0;` buffer free / `KY1;` full; all-spaces stops sending. Prosigns `[`=BT `_`=AR `<`=AS `#`=HH `>`=SK `=`=KN `\`=BK `%`=SN | 1_00_021; compat 1_02_004 | speed 0 -> `?;` |
| LC | `LC;` | – | 32 chars of the 16x2 LCD (custom glyphs -> spaces, VFO A/B kept) | 1_02_000 | |
| MD | `MD;` | `MDn;` | 1 LSB, 2 USB, 3 CW, 5 AM (RX only), 6 FSK (=DiGi), 7 CWR, 8 SWR Tune, 9 FSR (DiGi reverse) | SSB 1_02_000; 5,8 1_04_000 | No 4/FM. After MD8 the Get returns prior mode; exit Tune by setting previous mode (fw log says MD0 [UNVERIFIED]) [field] |
| ML | – | `MLn;` | list values of list type n, `|` delimited, e.g. `MLStraight|IAMBIC A|IAMBIC B|Ultimatic;` | 1_02_006 | menu discovery |
| MM | `MM<path>;` | `MM<path>=<value>;` | Query `MM<path>?;` -> `MM<type>|<len/listtype>|<name>;` | 1_02_006 | see Menu Manager below |
| MU | – | `MU;` | reload configuration (apply MM writes) | 1_04_000 | **drops IQ mode (Q9)** [field] |
| OM | `OM;` | – | `OMQC;` | 1_02_002 | model |
| PC | `PC;` | – | output power in 0.1 W (`PC45;` = 4.5 W; 3 digits ≥10 W) | 1_02_000 | **read-only; no power set** |
| PL | `PL;` | `PLdiv|mult|num|den;` | Get: `PLfreq|div|mult|num|den;` RX synth PLL. Set only if within 500 Hz of current; until next retune | 1_03_000 | reveals LO = dial − 12 kHz (− CW offset) |
| PS | `PS;` | `PS0;` | Get always `PS1;`; `PS0;` powers off | 1_04_000 | |
| Q0 | `Q0;` | `Q0<Hz>;` | TCXO freq 24 999 000-25 001 000 | QDX legacy | session only |
| Q1 | `Q1;` | `Q1n;` | sideband: Get 0=USB 1=LSB; Set 1=LSB else USB | | session |
| Q2 | `Q2;` | `Q2<Hz>;` | = FA | fixed 1_02_006 | |
| Q3 | `Q3;` | `Q3n;` | VOX enable 1/0 | | session |
| Q4 | `Q4;` | `Q4nn;` | Digi TX rise threshold % (80) | | session |
| Q5 | `Q5;` | `Q5nn;` | Digi TX fall threshold % (60) | | session |
| Q6 | `Q6;` | `Q6n;` | Digi min cycles (1) | | session |
| Q7 | `Q7;` | `Q7n;` | Digi min samples (480) | | session |
| Q8 | `Q8;` | `Q8n;` | Digi discard (1) | | session |
| Q9 | `Q9;` | `Q9n;` | **IQ mode** 1 on / 0 off | | session; see iq-mode.md |
| QA | `QA;` | `QAn;` | Japanese band limits | | session |
| QB | `QB;` | `QBn;` | CAT timeout enable | | |
| QC | `QC;` | `QCn;` | CAT timeout seconds | | |
| QJ | `QJ;` | `QJn;` | Digi TX shift threshold (mHz) | | |
| RC | – | `RC;` | clear RIT to 0 | 1_00_021 | |
| RD | – | `RDnnn;` | RIT down: absolute (−nnn) or relative (menu "CAT RU and RD") | early / rel. 1_02_001 | |
| RG | `RG;` | `RGnn;` | RF gain dB of current band (reply `RG063;`) | 1_02_006 | |
| RR | `RR;` | `RRn;` | RIT step 4=1k 5=500 6=100 7=10 8=1 Hz | 1_04_000 | |
| RT | `RT;` | `RTn;` | RIT off/on | set 1_00_021 | |
| RU | – | `RUnnn;` | RIT up (abs/rel) | | |
| RX | – | `RX;` | receive now (= TQ0) | early | hard key-up if TA0 not sent first |
| SA | `SA;` | – | AGC attenuation dB | 1_02_000 | |
| SM | `SM;` | – | S-meter in dB | 1_02_000 | format not detailed [UNVERIFIED] |
| SP | `SP;` | `SPn;` | split off/on | early | |
| SR | `SR;` | `SRn;` | Get SWR protection latch 0/1; Set (any n) resets | 1_04_004 | |
| SS | `SS;` | `SSn;` | SSB TX source 0 USB audio, 1 two-tone, 2 ext mic | 1_02_000 | "Auto" only via menu |
| SW | `SW;` | – | SWR ×100 (`SW121;`) during TX; `SW;` in RX | 1_02_000 | |
| TA | – | `TA<Hz.frac>;` | Digi: TX tone = dial + f; first TA keys down (shaped), `TA<10` keys up | 1_02_004 | sequence FA;TX;TA..;TA0;wait 5 ms;RX; |
| TB | `TB;` | – | `TBtnns;` t = KY chars left (0-9), nn count, s decoded CW text; 40-char non-circular buffer | 1_02_002 | |
| TM | `TM;` | `TMhhmmss;` | RTC | | |
| TQ | `TQ;` | `TQn;` | TX state 0 RX / 1 TX | early | |
| TR | `TR;` | `TRn;` | tune step 0=10M 1=1M 2=100k 3=10k 4=1k 5=500 6=100 7=10 Hz | 1_04_000 | |
| TX | – | `TX;` | transmit now (= TQ1) | early | SSB via CAT fixed 1_04_014 |
| UI | `UI;` | – | 24 hex chars STM32 unique ID | 1_04_003 | good for device identity |
| VN | `VN;` | – | `VN1_00_021QMX;` style | early | |

Absent from the 1_04_004 manual (so assume unsupported): PC set (power), AN, RA, PA, NB, NR,
XT, memory-channel commands, FM mode.

## Menu Manager (MM / ML)

- Path = menu names separated by `|`, case-insensitive, or 0-based indices, or mixed.
  Numeric names (e.g. filter "50") must be addressed by index.
- Grid pages (Band config., 16 columns) need a subscript: `MMBand config.|RF gain (db)[3];`
- Get: `MMAUDIO|AGC SETTINGS|THRESHOLD S;` -> `MM4;`. Sub-menu/app -> `?;`.
- Set: `MMAUDIO|AGC SETTINGS|THRESHOLD S=5;` -> stored in EEPROM. Applied immediately only if
  System config / CAT config "MM effect" = Immediate; otherwise on menu exit or `MU;`.
- Query: `MM0?;` -> `MM0|0|Audio;` (type|len or listtype|name). Types: 0 sub-menu,
  1 application, 2 string, 3 number, 4 byte, 5 list, 6 info, 7 mask (8 booleans).
  Grid: `MM12?;` -> `MM0|0|Band config.[16];`
- Useful paths for an emulator [some from field code]:
  - `MMProtection|Max. PA voltage;` / `=<V>;` — power control (field: SteffenLav)
  - `MMCW|CW offset;` — CW offset (field: SDR++-iak, SteffenLav)
  - `MMBand config.|RF gain (db)[n];`, Frequency min./max. per band
  - IQ Mode also lives in System config (persistent alternative to Q9) — exact path to be
    discovered with `MM?` queries on hardware [UNVERIFIED].
- After MU or an Immediate MM write, re-assert `Q91;` [field].

## Suggested emulator polling (based on field practice)

- 100 ms: `IF;` (freq, mode, TX, split, RIT)
- 250 ms: `SM;` in RX, `PC;SW;` in TX
- 1 s: `FA;FB;FT;`, `Q9;` (verify IQ still on)
- on connect: `VN;UI;OM;ID;Q91;` then `Q9;` readback, `Q30;` (VOX off), `MMCW|CW offset;`
