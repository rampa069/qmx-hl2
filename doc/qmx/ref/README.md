# QMX reference files

The QRP Labs documents used while writing `../*.md` are not copied into this repository. Read them
at their sources (versions used: 2026-09-27).

| Document | Source URL | Content |
|---|---|---|
| cat_1_04_004.pdf / .txt | https://qrp-labs.com/images/qmx/manuals/cat_1_04_004.pdf | CAT programming manual rev 1_04_004 (23-Jul-2026), all QMX-series |
| operation_1_04_004.pdf / .txt | https://qrp-labs.com/images/qmx/manuals/operation_1_04_004.pdf | Operating manual for firmware 1_04_004+ |
| VirtualU3S_1_04_008a.pdf / .txt | https://qrp-labs.com/images/qmx/manuals/VirtualU3S_1_04_008a.pdf | Virtual U3S beacon manual (1_04_008a) |
| schematics_rev5.pdf | https://qrp-labs.com/images/qmx/manuals/schematics_rev5.pdf | QMX schematics rev 5 (image PDF, no text layer) |
| qmx.html / qmx_page.txt | https://qrp-labs.com/qmx.html | QMX product page incl. **full firmware release notes** (1_00_0xx to 1_04_015) |
| qmxp.html / qmxp_page.txt | https://qrp-labs.com/qmxp.html | QMX+ product page (160-6 m, specs, latency note) |
| qmxp_ssbbeta.html / qmxp_ssbbeta_page.txt | https://qrp-labs.com/qmxp/ssbbeta.html | SSB (polar modulation) design/beta notes |

Other sources consulted (not downloaded):
- groups.io QRPLabs: "QMX IQ-mode question(s)" https://groups.io/g/QRPLabs/topic/105480964 ;
  "QMX TX'ing with a 3kHz pan adapter in HDSDR" https://groups.io/g/QRPLabs/topic/107653403 ;
  "firmware release 1_00_024" https://groups.io/g/QRPLabs/topic/107749436 ;
  "QMX remote SDR with CW transmit" https://groups.io/g/QRPLabs/topic/118317255
- SDR++ fork with QMX source (libqmx; Linux/macOS/Windows/Android backends):
  https://github.com/bubnikv/SDRPlusPlus-iak (source_modules/qmx_source, SYNCHRONIZATION.md)
- Tab5 QMX panadapter (IQ decode, CAT handshakes, TX via TA): https://github.com/SteffenLav/qmx-panadapter ,
  derivative with OpenHPSDR backend: https://github.com/pd4hs/qmx-panadapter
- SDR++ issue "Possible QMX+ source": https://github.com/AlexandreRouma/SDRPlusPlus/issues/1734
- https://www.lloydm.net/Demos/QMX_Interface.html
- Hamlib riglist.h / rigs/kenwood/ts480.c (model 2057 QRPLABS_QMX)
- Older manuals: https://qrp-labs.com/images/qmx/manuals/ (operation_1_0x_xxx.pdf, cat_1_0x_xxx.pdf)
