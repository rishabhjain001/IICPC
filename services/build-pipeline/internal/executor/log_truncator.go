package executor

import "fmt"

// MaxLogBytes is the maximum size of a build log that will be stored.
// Any log exceeding this size is truncated using middle-truncation so that
// both the beginning and the end of the log are preserved (Requirement 2.4).
const MaxLogBytes = 10 * 1024 * 1024 // 10 MiB

// TruncateMiddle returns log unchanged if len(log) <= MaxLogBytes.
// Otherwise it removes the middle section and inserts the sentinel line:
//
//	"\n... [truncated N bytes from middle] ...\n"
//
// The first MaxLogBytes/2 bytes and the last MaxLogBytes/2 bytes are
// preserved, separated by the sentinel.
func TruncateMiddle(log []byte) []byte {
	if len(log) <= MaxLogBytes {
		return log
	}

	half := MaxLogBytes / 2
	removed := len(log) - MaxLogBytes

	head := log[:half]
	tail := log[len(log)-half:]

	sentinel := []byte(fmt.Sprintf("\n... [truncated %d bytes from middle] ...\n", removed))

	result := make([]byte, 0, half+len(sentinel)+half)
	result = append(result, head...)
	result = append(result, sentinel...)
	result = append(result, tail...)
	return result
}
