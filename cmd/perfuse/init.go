package main

import (
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Getting from a downloaded binary to a running service.
//
// This is the step that loses people. Everything else in this project can be excellent and it does not
// matter if the first thing somebody meets is a flag they have to guess and no idea where to put files.
// Mirth ships an installer; Perfuse shipped a binary and a hope.
//
// So: one command that creates a directory layout, an example channel, and the service definition for
// whatever operating system it is running on. It prints the next command to run rather than claiming to
// have finished, because a tool that says "installed" and leaves a service unstarted is worse than one
// that hands over clearly.
//
// # It refuses to overwrite
//
// Everything here is idempotent-by-refusal rather than idempotent-by-overwrite. Running init twice in the
// wrong directory must not silently replace a working configuration, and a hospital's channel files are
// the most valuable thing on the box. -force exists, names what it will replace, and is not the default.

func cmdInit(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		dir     = fs.String("dir", ".", "directory to set up")
		system  = fs.String("service", "", "service definition to write: systemd, launchd, windows or none")
		user    = fs.String("user", "perfuse", "account the service runs as (systemd only)")
		addr    = fs.String("addr", defaultPlainAddr, "address the interface listens on")
		force   = fs.Bool("force", false, "replace files that already exist")
		example = fs.Bool("example", true, "write an example channel")
	)

	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage of init:
  perfuse init [flags]

Creates the directory layout, an example channel, and a service definition for
this operating system. Nothing that already exists is replaced unless you pass
-force.

  perfuse init                          # set up the current directory
  perfuse init -dir /opt/perfuse        # somewhere else
  perfuse init -service systemd         # write a unit file too
  perfuse init -service none            # just the directories

Flags:
`)
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return err
	}

	root, err := filepath.Abs(*dir)
	if err != nil {
		return err
	}

	// Chosen for the operating system rather than asked for, because somebody who has to be told which
	// service manager they have does not know, and somebody who knows can pass the flag.
	svc := strings.ToLower(strings.TrimSpace(*system))
	if svc == "" {
		svc = defaultServiceKind()
	}

	fmt.Fprintf(stdout, "Setting up Perfuse in %s\n\n", root)

	var created, skipped []string

	for _, sub := range []string{"channels", "data", "logs", "certs"} {
		path := filepath.Join(root, sub)
		existed, err := ensureDir(path)
		if err != nil {
			return err
		}
		if existed {
			skipped = append(skipped, sub+"/")
		} else {
			created = append(created, sub+"/")
		}
	}

	if *example {
		path := filepath.Join(root, "channels", "example.yaml")
		wrote, err := writeIfAbsent(path, exampleChannel(), 0o644, *force)
		if err != nil {
			return err
		}
		record(&created, &skipped, "channels/example.yaml", wrote)
	}

	// An environment file rather than flags in the unit, so a password or an S3 key never appears in a
	// process listing where anybody on the box can read it.
	envPath := filepath.Join(root, "perfuse.env")
	wrote, err := writeIfAbsent(envPath, environmentFile(root, *addr), 0o600, *force)
	if err != nil {
		return err
	}
	record(&created, &skipped, "perfuse.env", wrote)

	switch svc {
	case "systemd":
		wrote, err = writeIfAbsent(filepath.Join(root, "perfuse.service"),
			systemdUnit(root, *user), 0o644, *force)
		if err != nil {
			return err
		}
		record(&created, &skipped, "perfuse.service", wrote)

	case "launchd":
		wrote, err = writeIfAbsent(filepath.Join(root, "com.perfuse.server.plist"),
			launchdPlist(root, *addr), 0o644, *force)
		if err != nil {
			return err
		}
		record(&created, &skipped, "com.perfuse.server.plist", wrote)

	case "windows":
		wrote, err = writeIfAbsent(filepath.Join(root, "install-service.ps1"),
			windowsService(root, *addr), 0o644, *force)
		if err != nil {
			return err
		}
		record(&created, &skipped, "install-service.ps1", wrote)

	case "none":
		// Asked for nothing, so nothing is written.

	default:
		return fmt.Errorf("unknown service kind %q; use systemd, launchd, windows or none", svc)
	}

	for _, name := range created {
		fmt.Fprintf(stdout, "  created  %s\n", name)
	}
	for _, name := range skipped {
		// Named individually rather than counted. "3 files skipped" makes somebody wonder which, and
		// whether the one they cared about was among them.
		fmt.Fprintf(stdout, "  kept     %s (already there)\n", name)
	}

	writeNextSteps(stdout, root, svc, *addr)
	return nil
}

func record(created, skipped *[]string, name string, wrote bool) {
	if wrote {
		*created = append(*created, name)
	} else {
		*skipped = append(*skipped, name)
	}
}

func defaultServiceKind() string {
	switch runtime.GOOS {
	case "linux":
		return "systemd"
	case "darwin":
		return "launchd"
	case "windows":
		return "windows"
	default:
		return "none"
	}
}

func ensureDir(path string) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return true, nil
	}
	return false, os.MkdirAll(path, 0o755)
}

// writeIfAbsent writes a file unless it exists, reporting whether it wrote.
//
// The refusal is the point. A hospital's channel files are the most valuable thing on the box, and
// running init twice in the wrong directory must not replace a working configuration.
func writeIfAbsent(path, body string, mode os.FileMode, force bool) (bool, error) {
	if !force {
		if _, err := os.Stat(path); err == nil {
			return false, nil
		}
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		return false, err
	}
	return true, nil
}

// writeNextSteps prints the commands to run, for this operating system.
//
// Printed rather than performed. Installing a service needs privileges this process may not have, and a
// command that silently asks for root is a command people stop trusting. Showing what it would do lets
// somebody read it first.
func writeNextSteps(w io.Writer, root, svc, addr string) {
	fmt.Fprintf(w, "\nNext:\n\n")

	switch svc {
	case "systemd":
		fmt.Fprintf(w, "  1. Create the account the service runs as:\n")
		fmt.Fprintf(w, "       sudo useradd --system --home %s --shell /usr/sbin/nologin perfuse\n", root)
		fmt.Fprintf(w, "       sudo chown -R perfuse:perfuse %s\n\n", root)
		fmt.Fprintf(w, "  2. Install and start it:\n")
		fmt.Fprintf(w, "       sudo cp %s/perfuse.service /etc/systemd/system/\n", root)
		fmt.Fprintf(w, "       sudo systemctl daemon-reload\n")
		fmt.Fprintf(w, "       sudo systemctl enable --now perfuse\n\n")
		fmt.Fprintf(w, "  3. Watch it come up:\n")
		fmt.Fprintf(w, "       sudo journalctl -u perfuse -f\n\n")

	case "launchd":
		fmt.Fprintf(w, "  1. Install and start it:\n")
		fmt.Fprintf(w, "       cp %s/com.perfuse.server.plist ~/Library/LaunchAgents/\n", root)
		fmt.Fprintf(w, "       launchctl load ~/Library/LaunchAgents/com.perfuse.server.plist\n\n")
		fmt.Fprintf(w, "  2. Watch it come up:\n")
		fmt.Fprintf(w, "       tail -f %s/logs/perfuse.log\n\n", root)

	case "windows":
		fmt.Fprintf(w, "  1. In an elevated PowerShell:\n")
		fmt.Fprintf(w, "       cd %s\n", root)
		fmt.Fprintf(w, "       .\\install-service.ps1\n\n")
		fmt.Fprintf(w, "  2. Watch it come up:\n")
		fmt.Fprintf(w, "       Get-Content %s\\logs\\perfuse.log -Wait\n\n", root)

	default:
		fmt.Fprintf(w, "  Run it in the foreground:\n")
		fmt.Fprintf(w, "       perfuse serve -channels %s/channels -db %s/data/perfuse.db\n\n",
			root, root)
	}

	fmt.Fprintf(w, "Then open http://%s and sign in. The first run prints an administrator\n", addr)
	fmt.Fprintf(w, "password once, so read the log.\n")

	// Said here rather than left to a documentation page, because the default binds to localhost and
	// somebody who wants it reachable will otherwise change it without reading anything about TLS.
	if strings.HasPrefix(addr, "127.0.0.1") || strings.HasPrefix(addr, "localhost") {
		fmt.Fprintf(w, "\nIt is listening on localhost only. To reach it from another machine, change\n")
		fmt.Fprintf(w, "PERFUSE_ADDR in perfuse.env - and put TLS in front of it first, because the\n")
		fmt.Fprintf(w, "sign-in password would otherwise cross the network in the clear.\n")
	}
}

func environmentFile(root, addr string) string {
	// A generated session secret, because the alternative is a default that everybody shares. A shared
	// default signing key means a session cookie minted on one Perfuse is accepted by another.
	secret := randomSecret()

	return `# Perfuse configuration. Read by the service definition, so a value with a
# password in it never appears in a process listing.
#
# Anything in here can also be passed as a flag; the flag wins.

PERFUSE_ADDR=` + addr + `
PERFUSE_CHANNELS=` + filepath.Join(root, "channels") + `
PERFUSE_DB=` + filepath.Join(root, "data", "perfuse.db") + `

# Signs session cookies. Generated for this installation - if two servers share
# one, a session from either is accepted by both.
PERFUSE_SESSION_KEY=` + secret + `

# Credentials referenced from channel files as ${NAME}, so a secret lives here
# rather than in a file that goes into version control.
# AWS_ACCESS_KEY_ID=
# AWS_SECRET_ACCESS_KEY=
`
}

// randomSecret makes a session key.
//
// crypto/rand and no fallback: a predictable session key is a full authentication bypass, so failing to
// generate one has to stop the command rather than quietly produce something weaker.
func randomSecret() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		// Cannot happen on any supported platform, and if it did, a guessable key must not be written.
		panic("cannot generate a session key: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

func systemdUnit(root, user string) string {
	return `[Unit]
Description=Perfuse healthcare integration engine
Documentation=https://github.com/biodream-llc/perfuse
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=` + user + `
Group=` + user + `
WorkingDirectory=` + root + `
EnvironmentFile=` + filepath.Join(root, "perfuse.env") + `
ExecStart=/usr/local/bin/perfuse serve

# Reloads channel files without dropping connections.
ExecReload=/bin/kill -HUP $MAINPID

# Perfuse reports not-ready for this long after SIGTERM so a load balancer can
# stop sending it work, then drains in-flight messages. The stop timeout has to
# exceed that or systemd kills it mid-message.
KillSignal=SIGTERM
TimeoutStopSec=90

Restart=on-failure
RestartSec=5s

# A message that cannot be delivered is retried, so a crash loop should not be
# fast enough to fill the disk with logs.
StartLimitBurst=5
StartLimitIntervalSec=300

# It needs to read channel files, write a database and open sockets. Nothing
# else, so nothing else is permitted. Each line below has cost somebody an
# outage in some other project, which is why they are here rather than in a
# hardening guide nobody reads.
NoNewPrivileges=true
PrivateTmp=true
PrivateDevices=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
RestrictNamespaces=true
LockPersonality=true
MemoryDenyWriteExecute=false
ReadWritePaths=` + root + `

# MLLP listeners are usually above 1024. If one is not, add
# AmbientCapabilities=CAP_NET_BIND_SERVICE rather than running as root.
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX

[Install]
WantedBy=multi-user.target
`
}

func launchdPlist(root, addr string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.perfuse.server</string>

  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/bin/perfuse</string>
    <string>serve</string>
    <string>-addr</string>
    <string>` + addr + `</string>
    <string>-channels</string>
    <string>` + filepath.Join(root, "channels") + `</string>
    <string>-db</string>
    <string>` + filepath.Join(root, "data", "perfuse.db") + `</string>
  </array>

  <key>WorkingDirectory</key>
  <string>` + root + `</string>

  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key>
    <false/>
  </dict>

  <!-- launchd has no EnvironmentFile, so flags are used above and secrets have
       to come from EnvironmentVariables here. That does put them in a file
       readable by this user, which is why launchd is for a workstation or a
       test box rather than for production. -->

  <key>StandardOutPath</key>
  <string>` + filepath.Join(root, "logs", "perfuse.log") + `</string>
  <key>StandardErrorPath</key>
  <string>` + filepath.Join(root, "logs", "perfuse.log") + `</string>

  <!-- Long enough for in-flight messages to finish. -->
  <key>ExitTimeOut</key>
  <integer>90</integer>
</dict>
</plist>
`
}

func windowsService(root, addr string) string {
	// A PowerShell script using sc.exe rather than a wrapper binary, because Windows-heavy hospital IT is
	// exactly the audience that cannot install an unsigned third-party service wrapper, and sc.exe is
	// already there and already approved.
	return `# Installs Perfuse as a Windows service. Run in an elevated PowerShell.
#
# Uses sc.exe rather than a service wrapper, because a hospital that will not
# approve an unsigned third-party binary will still allow this.

$ErrorActionPreference = 'Stop'

$root     = '` + root + `'
$exe      = 'C:\Program Files\Perfuse\perfuse.exe'
$name     = 'Perfuse'
$channels = Join-Path $root 'channels'
$db       = Join-Path $root 'data\perfuse.db'

if (-not (Test-Path $exe)) {
  throw "perfuse.exe was not found at $exe. Copy it there, or edit this script."
}

# Quoting matters: paths contain spaces, and sc.exe needs the whole command as
# one argument with the inner quotes preserved.
$command = '"' + $exe + '" serve -addr ` + addr + ` -channels "' + $channels + '" -db "' + $db + '"'

if (Get-Service -Name $name -ErrorAction SilentlyContinue) {
  Write-Host "The $name service already exists. Stopping it to update the configuration."
  Stop-Service -Name $name -Force -ErrorAction SilentlyContinue
  sc.exe delete $name | Out-Null
  Start-Sleep -Seconds 2
}

sc.exe create $name binPath= $command start= auto DisplayName= "Perfuse Integration Engine" | Out-Null
sc.exe description $name "Healthcare integration engine. Routes and transforms HL7, X12 and FHIR." | Out-Null

# Restart on failure, with a delay so a crash loop cannot fill the disk.
sc.exe failure $name reset= 300 actions= restart/5000/restart/10000/restart/30000 | Out-Null

# The service account needs to write the database and the logs. LocalService is
# the least-privileged account that can still open a listening socket.
$acl = Get-Acl $root
$rule = New-Object System.Security.AccessControl.FileSystemAccessRule(
  'NT AUTHORITY\LocalService', 'Modify', 'ContainerInherit,ObjectInherit', 'None', 'Allow')
$acl.SetAccessRule($rule)
Set-Acl -Path $root -AclObject $acl

sc.exe config $name obj= 'NT AUTHORITY\LocalService' | Out-Null

Start-Service -Name $name
Get-Service -Name $name

Write-Host ''
Write-Host 'Perfuse is running. Open http://` + addr + `'
Write-Host 'The first run writes an administrator password to the log once:'
Write-Host ('  Get-Content ' + (Join-Path $root 'logs\perfuse.log'))
`
}

func exampleChannel() string {
	return `# An example channel, written to be read rather than to be impressive.
#
# It listens for HL7 over MLLP and writes each message to a file. That is the
# smallest thing that proves the engine works end to end, and you can send it a
# message with:
#
#   perfuse send -addr 127.0.0.1:2575 examples/adt.hl7
#
# Delete this file once you have a real channel.

name: example
description: Accepts HL7 over MLLP and files it, to prove the engine is working

source:
  type: mllp
  # Localhost only. Change it when you know what should be allowed to connect,
  # and remember MLLP has no authentication or encryption of its own.
  listen: 127.0.0.1:2575

destinations:
  - name: to-disk
    type: file
    dir: ./data/received
`
}
