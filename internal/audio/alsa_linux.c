// SPDX-License-Identifier: GPL-2.0-or-later
// Copyright (C) 2026 Ramon Martinez

// ALSA error handler: alsa-lib prints its errors to stderr by default. The static Linux build
// cannot load ALSA plugins (pipewire, pulse, jack), so enumeration printed about 30 "Dynamic
// loading not supported" lines at every start. Route them to the Go logger instead.

#include <stdarg.h>
#include <stdio.h>

extern void goALSAError(char *msg);

typedef void (*snd_lib_error_handler_t)(const char *file, int line, const char *function,
                                        int err, const char *fmt, ...);
extern int snd_lib_error_set_handler(snd_lib_error_handler_t handler);

static void qmxhl2_alsa_error(const char *file, int line, const char *function, int err,
                              const char *fmt, ...) {
    char msg[512];
    char out[640];
    va_list ap;
    (void)file;
    (void)line;
    (void)err;
    va_start(ap, fmt);
    vsnprintf(msg, sizeof msg, fmt, ap);
    va_end(ap);
    snprintf(out, sizeof out, "%s: %s", function ? function : "?", msg);
    goALSAError(out);
}

void qmxhl2_quiet_alsa(void) { snd_lib_error_set_handler(qmxhl2_alsa_error); }
