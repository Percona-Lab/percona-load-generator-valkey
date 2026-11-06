package valkey

import "strings"

// SlotCount is the number of hash slots in a Valkey cluster.
const SlotCount = 16384

// crc16 table for the XMODEM variant Valkey cluster uses (poly 0x1021, init 0).
var crc16tab = func() [256]uint16 {
	var t [256]uint16
	for i := 0; i < 256; i++ {
		c := uint16(i) << 8
		for j := 0; j < 8; j++ {
			if c&0x8000 != 0 {
				c = c<<1 ^ 0x1021
			} else {
				c <<= 1
			}
		}
		t[i] = c
	}
	return t
}()

func crc16(s string) uint16 {
	var crc uint16
	for i := 0; i < len(s); i++ {
		crc = crc<<8 ^ crc16tab[byte(crc>>8)^s[i]]
	}
	return crc
}

// Slot returns the cluster slot of a key. If the key has a non-empty hash tag
// ("{...}" after the first "{"), only the tag is hashed.
func Slot(key string) int {
	if i := strings.IndexByte(key, '{'); i >= 0 {
		if j := strings.IndexByte(key[i+1:], '}'); j > 0 {
			key = key[i+1 : i+1+j]
		}
	}
	return int(crc16(key) % SlotCount)
}

// RouteKey returns the key a command is routed by (its first key), or "" for
// commands without a key.
func RouteKey(cmd Cmd) string {
	if idx := KeyIndexes(cmd); len(idx) > 0 {
		return cmd[idx[0]]
	}
	return ""
}

// Router maps a key to the index of the node that serves it, or -1 when it
// cannot tell. It is used to split client-side results per node.
type Router interface {
	NodeIndex(key string) int
}
