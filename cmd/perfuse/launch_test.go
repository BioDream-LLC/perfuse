package main

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestADoubleClickLaunchServesFromTheUserDataFolder(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LOCALAPPDATA", dir)

	if perfuseAnswers(launchURL(), 200*time.Millisecond) {
		t.Skip("something already answers at " + launchURL())
	}

	var served []string
	var out bytes.Buffer
	err := runLaunch(&out, &out, func(string) error { return nil }, func(args []string) error {
		served = args
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"serve", "-db", filepath.Join(dir, "Perfuse", "perfuse.db"), "-channels", filepath.Join(dir, "Perfuse", "channels")}
	if strings.Join(served, " ") != strings.Join(want, " ") {
		t.Fatalf("served %q, want %q", served, want)
	}
	if !strings.Contains(out.String(), launchURL()) {
		t.Errorf("the window does not say where Perfuse is:\n%s", out.String())
	}
}

func TestTheBrowserOpensOnlyOncePerfuseAnswers(t *testing.T) {
	alive := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/livez" && alive {
			_, _ = w.Write([]byte(`{"status":"alive"}`))
			return
		}
		http.Error(w, "starting", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	if perfuseAnswers(srv.URL+"/", time.Second) {
		t.Fatal("a server that is not yet alive was taken for Perfuse")
	}
	alive = true
	if !perfuseAnswers(srv.URL+"/", time.Second) {
		t.Fatal("a live Perfuse was not recognised")
	}
}

func TestAnotherProgramOnThePortIsNotTakenForPerfuse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	if perfuseAnswers(srv.URL+"/", time.Second) {
		t.Fatal("a server answering 200 ok to everything was taken for Perfuse")
	}
}

func TestASecondDoubleClickOpensTheRunningServer(t *testing.T) {
	ln, err := net.Listen("tcp", defaultPlainAddr)
	if err != nil {
		t.Skip("the default port is in use: " + err.Error())
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"alive"}`))
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	var opened string
	var out bytes.Buffer
	err = runLaunch(&out, &out, func(u string) error { opened = u; return nil }, func([]string) error {
		t.Fatal("a second server was started")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if opened != launchURL() {
		t.Fatalf("opened %q, want %q", opened, launchURL())
	}
}
