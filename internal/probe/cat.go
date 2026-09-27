package probe

import (
	"fmt"
	"strings"
	"time"
)

// ReadWriter is the subset of serial.Port the probe needs. Read returns (0, nil) on timeout.
type ReadWriter interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
}

// Query sends a CAT command such as "VN;" and waits for the reply that starts with the same
// two-letter prefix, skipping any unrelated ';'-terminated messages. It is a stopgap for the
// probe; the real CAT client (QMX-dfb.3) replaces it.
func Query(rw ReadWriter, cmd string, timeout time.Duration) (string, error) {
	if !strings.HasSuffix(cmd, ";") {
		cmd += ";"
	}
	if len(cmd) < 3 {
		return "", fmt.Errorf("CAT command %q too short", cmd)
	}
	prefix := cmd[:2]
	if _, err := rw.Write([]byte(cmd)); err != nil {
		return "", fmt.Errorf("write %q: %w", cmd, err)
	}
	deadline := time.Now().Add(timeout)
	var acc strings.Builder
	buf := make([]byte, 256)
	for time.Now().Before(deadline) {
		n, err := rw.Read(buf)
		if err != nil {
			return "", fmt.Errorf("read reply to %q: %w", cmd, err)
		}
		acc.Write(buf[:n])
		for {
			s := acc.String()
			i := strings.IndexByte(s, ';')
			if i < 0 {
				break
			}
			msg := s[:i+1]
			acc.Reset()
			acc.WriteString(s[i+1:])
			if strings.HasPrefix(msg, prefix) {
				return msg, nil
			}
		}
	}
	return "", fmt.Errorf("no reply to %q within %v", cmd, timeout)
}

// Send writes a CAT set-command that has no reply.
func Send(rw ReadWriter, cmd string) error {
	if !strings.HasSuffix(cmd, ";") {
		cmd += ";"
	}
	_, err := rw.Write([]byte(cmd))
	return err
}
