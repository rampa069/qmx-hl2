// SPDX-License-Identifier: GPL-2.0-or-later
// Copyright (C) 2026 Ramon Martinez

package audio

/*
#cgo LDFLAGS: -lasound
void qmxhl2_quiet_alsa(void);
*/
import "C"

import "log/slog"

//export goALSAError
func goALSAError(msg *C.char) { slog.Debug("ALSA", "msg", C.GoString(msg)) }

// quietALSA sends alsa-lib's error messages to the debug log instead of stderr.
func quietALSA() { C.qmxhl2_quiet_alsa() }
