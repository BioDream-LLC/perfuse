package ftpconn

import (
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Reading, listing and moving files. The other half of the client, which until now could only upload.
//
// # Why MLSD is tried before LIST
//
// LIST has no specification. It returns whatever the server's operating system prints, which for a Unix server is ls
// output and for others is something else entirely. The date in ls output is the reason this matters: recent files show a
// time and no year, older ones show a year and no time, so half the entries have no year at all and the parser has to
// guess. Guessing wrong by a year does not break listing, but it does break the settle rule that decides when a file has
// finished being written, and that failure looks like messages occasionally arriving truncated.
//
// MLSD, from RFC 3659, returns a machine-readable listing with a full UTC timestamp. Most servers written in the last
// twenty years support it. So it is tried first and LIST is the fallback, with the year-guessing confined to one function
// that says what it is doing.

// Entry is one file or directory in a listing.
type Entry struct {
	Name    string
	Size    int64
	ModTime time.Time
	IsDir   bool

	// TimeIsApproximate is true when the listing did not give a year and one had to be inferred.
	//
	// Surfaced rather than hidden because a poller uses the modification time to decide whether a file has stopped
	// changing. A caller that knows the time is a guess can fall back to comparing sizes instead of silently trusting a
	// timestamp that may be a year out.
	TimeIsApproximate bool
}

// List returns the entries in a directory.
func (c *Client) List(dir string) ([]Entry, error) {
	if dir == "" {
		dir = "."
	}

	entries, err := c.listMLSD(dir)
	if err == nil {
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
		return entries, nil
	}

	// Only fall back for a server that does not know the command. Any other failure - no permission, no such directory -
	// would fail identically on LIST, and retrying it turns one clear error into two confusing ones.
	if !isUnsupported(err) {
		return nil, err
	}

	entries, err = c.listLIST(dir)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, nil
}

// listMLSD uses the machine-readable listing from RFC 3659.
func (c *Client) listMLSD(dir string) ([]Entry, error) {
	data, err := c.openPassive()
	if err != nil {
		return nil, err
	}
	defer data.Close()

	if _, _, err := c.commandExpect(125, 150, "MLSD "+dir); err != nil {
		return nil, err
	}

	body, err := io.ReadAll(io.LimitReader(data, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("reading the listing of %s: %w", dir, err)
	}
	_ = data.Close()

	if _, _, err := c.commandlessExpect(226, 250); err != nil {
		return nil, err
	}

	var out []Entry
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if e, ok := parseMLSD(line); ok {
			out = append(out, e)
		}
	}
	return out, nil
}

// parseMLSD reads one MLSD line: "fact=value;fact=value; filename".
func parseMLSD(line string) (Entry, bool) {
	sep := strings.Index(line, "; ")
	if sep < 0 {
		return Entry{}, false
	}

	name := line[sep+2:]
	if name == "." || name == ".." || name == "" {
		return Entry{}, false
	}

	e := Entry{Name: name}

	for _, fact := range strings.Split(line[:sep], ";") {
		eq := strings.Index(fact, "=")
		if eq < 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(fact[:eq]))
		val := fact[eq+1:]

		switch key {
		case "type":
			switch strings.ToLower(val) {
			case "dir", "cdir", "pdir":
				e.IsDir = true
			}
		case "size":
			if n, err := strconv.ParseInt(val, 10, 64); err == nil {
				e.Size = n
			}
		case "modify":
			// YYYYMMDDHHMMSS, always UTC per the specification, which is the whole reason MLSD is preferred.
			if t, err := time.Parse("20060102150405", val); err == nil {
				e.ModTime = t.UTC()
			}
		}
	}
	return e, true
}

// listLIST parses the unspecified ls-style listing.
func (c *Client) listLIST(dir string) ([]Entry, error) {
	data, err := c.openPassive()
	if err != nil {
		return nil, err
	}
	defer data.Close()

	if _, _, err := c.commandExpect(125, 150, "LIST "+dir); err != nil {
		return nil, err
	}

	body, err := io.ReadAll(io.LimitReader(data, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("reading the listing of %s: %w", dir, err)
	}
	_ = data.Close()

	if _, _, err := c.commandlessExpect(226, 250); err != nil {
		return nil, err
	}

	var out []Entry
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimRight(line, "\r")
		if e, ok := parseLIST(line); ok {
			out = append(out, e)
		}
	}
	return out, nil
}

// parseLIST reads one Unix-style listing line.
//
// The format has no specification. This handles the ls -l layout that the overwhelming majority of FTP servers produce
// and gives up on anything else rather than guessing, because a line parsed wrongly yields a plausible file with a wrong
// size, which the settle rule then trusts.
func parseLIST(line string) (Entry, bool) {
	fields := strings.Fields(line)
	if len(fields) < 9 {
		return Entry{}, false
	}

	perms := fields[0]
	if len(perms) < 10 {
		return Entry{}, false
	}

	e := Entry{IsDir: perms[0] == 'd'}

	// A symlink. Skipped rather than followed: FTP gives no way to stat the target, so its size and time are the link's,
	// and a poller would compare the wrong numbers.
	if perms[0] == 'l' {
		return Entry{}, false
	}

	size, err := strconv.ParseInt(fields[4], 10, 64)
	if err != nil {
		return Entry{}, false
	}
	e.Size = size

	// Name is everything after the date, joined back together because a filename may contain spaces.
	e.Name = strings.Join(fields[8:], " ")
	if e.Name == "." || e.Name == ".." || e.Name == "" {
		return Entry{}, false
	}

	e.ModTime, e.TimeIsApproximate = parseLISTTime(fields[5], fields[6], fields[7])
	return e, true
}

// parseLISTTime interprets the three date fields of an ls listing.
//
// The awkward part of the format. A file modified within roughly the last six months shows "Aug 27 12:00" with no year; an
// older one shows "Aug 27  2024" with no time. So a recent file's year has to be inferred, and inferring it as the current
// year is wrong every January for files written in December.
//
// Handled by assuming the most recent occurrence of that date that is not in the future, and reporting the result as
// approximate either way so a caller does not trust it for anything that matters.
func parseLISTTime(month, day, yearOrTime string) (time.Time, bool) {
	if strings.Contains(yearOrTime, ":") {
		// No year given. Assume the most recent occurrence that is not in the future, which is what ls itself means.
		now := time.Now().UTC()
		t, err := time.Parse("Jan _2 15:04", month+" "+day+" "+yearOrTime)
		if err != nil {
			return time.Time{}, true
		}
		guess := time.Date(now.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, time.UTC)
		if guess.After(now.Add(24 * time.Hour)) {
			// A date later in the year than today means it was last year. The day of slack absorbs a server clock
			// slightly ahead of ours, which is common and otherwise puts every new file a year in the past.
			guess = guess.AddDate(-1, 0, 0)
		}
		return guess, true
	}

	t, err := time.Parse("Jan _2 2006", month+" "+day+" "+yearOrTime)
	if err != nil {
		return time.Time{}, true
	}
	// A full date but no time, so it is only accurate to the day. Still approximate.
	return t, true
}

// Retrieve reads a file.
func (c *Client) Retrieve(remote string, max int64) ([]byte, error) {
	if err := c.binary(); err != nil {
		return nil, err
	}

	data, err := c.openPassive()
	if err != nil {
		return nil, err
	}
	defer data.Close()

	if _, _, err := c.commandExpect(125, 150, "RETR "+remote); err != nil {
		return nil, err
	}

	// One byte over the limit, so a file that is exactly at it is accepted and one past it is detected rather than
	// silently truncated.
	body, err := io.ReadAll(io.LimitReader(data, max+1))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", remote, err)
	}
	_ = data.Close()

	if _, _, err := c.commandlessExpect(226, 250); err != nil {
		return nil, err
	}

	if int64(len(body)) > max {
		return nil, fmt.Errorf("%s is larger than the maximum of %d bytes", remote, max)
	}
	return body, nil
}

// Delete removes a file.
func (c *Client) Delete(remote string) error {
	return c.cmd(250, "DELE "+remote)
}

// Rename moves a file. Both commands must succeed or the file is in neither place.
func (c *Client) Rename(from, to string) error {
	if _, _, err := c.commandExpect(350, 350, "RNFR "+from); err != nil {
		return fmt.Errorf("preparing to rename %s: %w", from, err)
	}
	if err := c.cmd(250, "RNTO "+to); err != nil {
		// The server is now holding a pending rename with no destination. Reported clearly because the file is still
		// where it was, which is the recoverable outcome, but the session should not be reused for another rename.
		return fmt.Errorf("renaming %s to %s failed, so the file is still where it was: %w", from, to, err)
	}
	return nil
}

// MakeDir creates a directory, treating "already exists" as success.
func (c *Client) MakeDir(dir string) error {
	dir = strings.Trim(dir, "/")
	if dir == "" || dir == "." {
		return nil
	}

	// Created one level at a time. There is no recursive MKD in FTP, and a server given a nested path either creates
	// only the last component or fails, depending on which server it is.
	var built string
	for _, part := range strings.Split(dir, "/") {
		if part == "" {
			continue
		}
		built = path.Join(built, part)
		if err := c.cmd(257, "MKD "+built); err != nil {
			// 550 covers both "already exists" and "permission denied", and FTP gives no way to tell them apart. Both
			// are ignored here: if it was permission, the operation that needed the directory will fail next and say so
			// with the actual filename, which is more useful than failing here.
			continue
		}
	}
	return nil
}

// binary switches to image mode.
//
// Necessary before any transfer. In the default ASCII mode some servers translate line endings, which for HL7 turns the
// carriage returns that separate segments into something else and produces one enormous segment at the far end.
func (c *Client) binary() error { return c.cmd(200, "TYPE I") }

// isUnsupported reports whether the server rejected a command it does not implement.
//
// 500 and 502 mean the command is unknown or not implemented. 504 means the parameter is not supported. Anything else is
// a real failure that would recur on the fallback.
func isUnsupported(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, code := range []string{"500", "502", "504"} {
		if strings.Contains(msg, code) {
			return true
		}
	}
	return false
}
