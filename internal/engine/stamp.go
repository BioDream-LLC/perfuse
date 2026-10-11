package engine

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"time"
)

// uniqueStamp is a suffix for a name that must not collide with another: the time, so names sort as they arrived, and
// random digits, because the time alone is not unique. Windows' clock ticks far more coarsely than a nanosecond, and two
// messages without a control id in one tick got one file name, the second replacing the first.
func uniqueStamp() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return strconv.FormatInt(time.Now().UnixNano(), 10) + "-" + hex.EncodeToString(b[:])
}
