package trafficimport

import "os"

func osWriteFile(p string, b []byte) error { return os.WriteFile(p, b, 0o600) }
