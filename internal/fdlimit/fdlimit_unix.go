//go:build unix

package fdlimit

import "golang.org/x/sys/unix"

// Raise lifts the soft limit to the hard limit and reports both.
//
// Raising its own soft limit is something a server is expected to do, and it is the difference between
// coping with a burst and falling over during one. The soft limit is a default for interactive shells,
// not a statement about what this process should be allowed; the hard limit is the real ceiling.
//
// A failure is returned in the report rather than as an error to abort on. A process that cannot raise its
// limit still works inside the limit it has - it simply has less room, which is exactly what the caller
// needs to know in order to bound itself.
func Raise() Report {
	var lim unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &lim); err != nil {
		return Report{Err: err}
	}

	r := Report{Before: uint64(lim.Cur), Soft: uint64(lim.Cur), Hard: uint64(lim.Max)}

	if lim.Cur >= lim.Max {
		return r
	}

	want := lim
	want.Cur = want.Max
	if err := unix.Setrlimit(unix.RLIMIT_NOFILE, &want); err != nil {
		// Reported and carried on with. On some systems the hard limit cannot be reached from an
		// unprivileged process even though it is advertised, and that is not a reason to refuse to start.
		r.Err = err
		return r
	}

	// Read back rather than assuming the write took. A kernel may clamp the value, and a plan made from a
	// number that was never applied is worse than a plan made from a small one.
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &lim); err != nil {
		r.Err = err
		return r
	}
	r.Soft = uint64(lim.Cur)
	r.Hard = uint64(lim.Max)
	r.Raised = r.Soft > r.Before
	return r
}
