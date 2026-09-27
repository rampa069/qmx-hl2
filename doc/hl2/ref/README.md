# Reference copies (HL2 / openHPSDR Protocol 1)

These are copies, not moves. The originals are under `/Volumes/FastDisk/Radio/HL2/`.

| File | Original path | What it is |
|---|---|---|
| `hl2-wiki-Protocol.md` | `zeus/docs/references/firmware/hermes-lite-2/wiki/Protocol.md` | HL2 wiki "Protocol" page (softerhardware), snapshot 2026-06-27. The primary HL2 extension spec |
| `zeus-hermes-lite2-protocol-annotated.md` | `zeus/docs/references/protocol-1/hermes-lite2-protocol.md` | Same wiki page, annotated by the Zeus project: 0x0b keyer register, PureSignal feedback path |
| `USB_protocol_V1.60.doc` / `.txt` | `zeus/docs/references/protocol-1/USB_protocol_V1.60.doc` (txt made with `textutil`) | openHPSDR USB protocol V1.60: 512-byte frame, C&C bit maps |
| `Metis-How_it_works_V1.33.pdf` / `.txt` | `OpenHPSDR-Firmware/Protocol 1/Documentation/Metis- How it works_V1.33.pdf` (txt via `pdftotext -layout`) | Metis UDP encapsulation: discovery, start/stop, 1032-byte packets, sequence numbers |
| `hl2-wiki-Thetis-I2C-Control.md` | `zeus/docs/references/firmware/hermes-lite-2/wiki/Thetis-I2C-Control.md` | I²C buses and addresses (Versa clock 0xd4, bias pot, N2ADR) |
| `hl2-wiki-IO.md` | `…/wiki/IO.md` | HL2 GPIO assignment |
| `hl2-wiki-Software.md` | `…/wiki/Software.md` | Client software list and HL2 feature support |
| `hl2-wiki-Thetis-Setup.md` | `…/wiki/Thetis-Setup.md` | Thetis HL2 setup notes |
| `hl2-wiki-PureSignal.md` | `…/wiki/PureSignal.md` | HL2 PureSignal |
| `hl2-wiki-Band-Volts.md` | `…/wiki/Band-Volts.md` | Band-volts PWM (0x00 bit 11) |
| `hl2-wiki-FAQ.md` | `…/wiki/FAQ.md` | HL2 FAQ |
| `deskhpsdr-Notes_if_using_HERMES-Lite-2.md` | `deskhpsdr/Notes_if_using_HERMES-Lite-2.md` | deskHPSDR HL2 user notes |
| `HL2IOBoard-README.md`, `HL2IOBoard-i2c_registers.h` | `HL2IOBoard/README.md`, `HL2IOBoard/i2c_registers.h` | N2ADR IO board I²C register map (addr 0x1d on bus 2), probed by pi/desk/Thetis |

Primary RTL sources were read in place, not copied. They are in `/Volumes/FastDisk/Radio/HL2/gateware/Original/gateware/rtl/`
(`dsopenhpsdr1.v`, `usopenhpsdr1.v`, `control.v`, `radio_openhpsdr1/radio.v`, `fifos.v`, `hermeslite_core.v`),
upstream softerhardware commit 7472bd1.
