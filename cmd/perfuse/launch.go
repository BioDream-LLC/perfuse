package main

import (
	"context"

	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Double-click launch.
//
// # Why
//
// On Windows the release is a perfuse.exe, and the first thing anyone does with an .exe is double-click it. With no arguments
// Perfuse printed its usage and exited, so Explorer opened a console, wrote a page of flags into it and closed it before it
// could be read: nothing happened, as far as anyone could see. A product whose first impression is nothing is not going to
// get a second one.
//
// So a double-click starts the server with everything defaulted and opens the browser at the sign-in page. Typing perfuse
// with no arguments in a terminal still prints the usage, because someone at a prompt asked for help, not for a server; the
// two are told apart by asking Windows whether this process owns its console (launchedByDoubleClick).
//
// # Where the data goes
//
// Not the working directory. Explorer runs a double-clicked program in its own folder, which is typically Downloads, and a
// database dropped into Downloads gets deleted with the next tidy-up. The per-user application data folder is where Windows
// programs keep their state, so a second double-click finds the same channels and users as the first.

// launchDirName is the folder under the user's local application data.
const launchDirName = "Perfuse"

// launchDataDir returns the folder a double-click launch keeps its database and channels in.
func launchDataDir() (string, error) {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		var err error
		if base, err = os.UserConfigDir(); err != nil {
			return "", fmt.Errorf("no folder for Perfuse's data: %w", err)
		}
	}

	return filepath.Join(base, launchDirName), nil
}

// launchArgs is the serve command a double-click runs.
func launchArgs(dir string) []string {
	return []string{
		"serve",
		"-db", filepath.Join(dir, "perfuse.db"),
		"-channels", filepath.Join(dir, "channels"),
	}
}

// launchURL is the address the browser is sent to: the default plain address on loopback, which is all a double-click serves.
func launchURL() string {
	return "http://" + defaultPlainAddr + "/"
}

// runLaunch starts Perfuse for someone who double-clicked it.
//
// openBrowser and serve are parameters so a test can run the whole sequence without a browser or a real server.
func runLaunch(stdout, stderr io.Writer, openBrowser func(string) error, serve func([]string) error) error {
	url := launchURL()

	// Already running - the first window is still open, or Perfuse runs as a service. Starting a second server would fail on
	// the port with an error that reads like something is broken; what the person wanted was the sign-in page.
	if perfuseAnswers(url, time.Second) {
		fmt.Fprintf(stdout, "Perfuse is already running at %s - opening it.\n", url)
		return openBrowser(url)
	}

	dir, err := launchDataDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, "channels"), 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}

	fmt.Fprintf(stdout, "\n  Perfuse is starting.\n\n"+
		"    Address:  %s\n"+
		"    Data:     %s\n\n"+
		"  Your browser will open at the sign-in page. Keep this window open while you use Perfuse;\n"+
		"  closing it stops the server. On the first run the administrator's password is shown below.\n\n", url, dir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		if waitForPerfuse(ctx, url, 60*time.Second) {
			if err := openBrowser(url); err != nil {
				fmt.Fprintf(stderr, "perfuse: could not open a browser (%v); open %s yourself\n", err, url)
			}
		}
	}()

	return serve(launchArgs(dir))
}

// waitForPerfuse polls until the server answers, the context ends or the limit passes.
func waitForPerfuse(ctx context.Context, url string, limit time.Duration) bool {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if perfuseAnswers(url, 500*time.Millisecond) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(250 * time.Millisecond):
		}
	}

	return false
}

// perfuseAnswers reports whether a Perfuse server is listening at url.
//
// Checked by its liveness endpoint rather than by the port being open, so that some other program on 8080 is not mistaken
// for Perfuse and the browser is not sent to it.
func perfuseAnswers(url string, timeout time.Duration) bool {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(strings.TrimSuffix(url, "/") + "/livez")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	return resp.StatusCode == http.StatusOK && strings.Contains(string(body), `"alive"`)
}
