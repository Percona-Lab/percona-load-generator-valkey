package stats

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
)

// ErrClass groups client-visible errors. Class 0 means no error.
type ErrClass uint8

// Error classes. Server error replies start with an upper-case code
// (MOVED, LOADING, ...); the rest are client or transport failures.
const (
	ErrNone ErrClass = iota
	ErrTimeout
	ErrConnection
	ErrMoved
	ErrAsk
	ErrLoading
	ErrOOM
	ErrReadOnly
	ErrNoAuth
	ErrNoPerm
	ErrBusy
	ErrClusterDown
	ErrTryAgain
	ErrWrongType
	ErrOther
	numErrClasses
)

// NumErrClasses is the number of classes including ErrNone.
const NumErrClasses = int(numErrClasses)

var errClassNames = [numErrClasses]string{
	"none", "timeout", "connection", "MOVED", "ASK", "LOADING", "OOM", "READONLY",
	"NOAUTH", "NOPERM", "BUSY", "CLUSTERDOWN", "TRYAGAIN", "WRONGTYPE", "other",
}

func (c ErrClass) String() string {
	if int(c) < len(errClassNames) {
		return errClassNames[c]
	}
	return "other"
}

var codeClass = map[string]ErrClass{
	"MOVED": ErrMoved, "ASK": ErrAsk, "LOADING": ErrLoading, "OOM": ErrOOM,
	"READONLY": ErrReadOnly, "NOAUTH": ErrNoAuth, "WRONGPASS": ErrNoAuth, "NOPERM": ErrNoPerm,
	"BUSY": ErrBusy, "BUSYGROUP": ErrBusy, "CLUSTERDOWN": ErrClusterDown, "TRYAGAIN": ErrTryAgain,
	"WRONGTYPE": ErrWrongType, "MASTERDOWN": ErrReadOnly,
}

// Classify maps an error to its class. A nil error is ErrNone.
func Classify(err error) ErrClass {
	if err == nil {
		return ErrNone
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrTimeout
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return ErrTimeout
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
		return ErrConnection
	}
	var oe *net.OpError
	if errors.As(err, &oe) {
		return ErrConnection
	}
	msg := err.Error()
	// Server error replies: "<CODE> text" (valkey-go and go-redis keep it).
	code := msg
	if i := strings.IndexByte(msg, ' '); i > 0 {
		code = msg[:i]
	}
	if c, ok := codeClass[code]; ok {
		return c
	}
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "i/o timeout"), strings.Contains(low, "deadline exceeded"):
		return ErrTimeout
	case strings.Contains(low, "connection reset"), strings.Contains(low, "broken pipe"),
		strings.Contains(low, "connection refused"), strings.Contains(low, "use of closed"),
		strings.Contains(low, "eof"):
		return ErrConnection
	}
	return ErrOther
}
