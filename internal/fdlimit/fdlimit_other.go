//go:build !unix

package fdlimit

// Raise reports that this platform has no descriptor limit to raise.
//
// Windows has no RLIMIT_NOFILE. Handles are bounded by memory rather than by a per-process count, so there
// is nothing to lift and nothing to plan against - which is why Unlimited is reported as a fact instead of
// being papered over with a large number. The caller still bounds its concurrency, because descriptors were
// never the only reason to.
func Raise() Report {
	return Report{Unlimited: true}
}
