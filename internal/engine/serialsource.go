package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	// Aliased because "serial" is already used as an identifier elsewhere in this package - a certificate serial number
	// in the TLS tests - and an import name shadows it for the whole package including its tests.
	serialport "go.bug.st/serial"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/framing"
)

// serialReader reads messages from a serial port.
//
// # A serial line has no connection to lose
//
// Every other source here can tell when the far end has gone: a socket returns EOF, a poll fails to list, an HTTP request
// is refused. A cable cannot. An unplugged cable, a device switched off, and a device with nothing to say are all silence,
// and there is no observation that distinguishes them.
//
// That shapes two things. quiet_after is the only way anybody learns a feed has died, so it is warned about when unset.
// And a read error does not mean the device is gone - it usually means the USB adapter was unplugged and replugged, which
// on every operating system returns the same device path with a different kernel handle, leaving the old one erroring
// forever. Reopening is the only recovery.
type serialReader struct {
	ch  *Channel
	cfg *config.SerialSource
	fr  framing.Settings
	log *slog.Logger

	stop   chan struct{}
	closed sync.Once
	wg     sync.WaitGroup

	mu     sync.Mutex
	port   serialport.Port
	stats  SerialStats
	lastRx time.Time
}

// SerialStats reports what the reader has done.
type SerialStats struct {
	Open          bool      `json:"open"`
	Messages      int64     `json:"messages"`
	FramingErrors int64     `json:"framingErrors"`
	Reopens       int64     `json:"reopens"`
	LastMessage   time.Time `json:"lastMessage,omitempty"`
	LastError     string    `json:"lastError,omitempty"`
	Framing       string    `json:"framing,omitempty"`

	// Quiet is true when nothing has arrived for longer than quiet_after.
	//
	// Reported as state rather than only logged, so the channel list can show it. A serial feed that has gone quiet looks
	// identical to one that is working until somebody asks.
	Quiet bool `json:"quiet"`
}

// startSerialSource opens the port and begins reading.
func (c *Channel) startSerialSource() error {
	cfg := c.cfg.Source.Serial
	if cfg == nil {
		return fmt.Errorf("channel %q has a serial source but no serial block", c.cfg.Name)
	}

	for _, w := range cfg.Warnings() {
		c.log.Warn("serial source", "channel", c.cfg.Name, "warning", w)
	}

	fr, err := cfg.Settings(cfg.MaxMessageSize)
	if err != nil {
		return fmt.Errorf("channel %q: %w", c.cfg.Name, err)
	}

	r := &serialReader{ch: c, cfg: cfg, fr: fr, log: c.log, stop: make(chan struct{})}
	r.stats.Framing = fr.Describe()

	// Opened now rather than in the loop, so a wrong device path or a port somebody else already has open is reported
	// while the channel is being started rather than in a log line five seconds later.
	port, err := r.open()
	if err != nil {
		return fmt.Errorf("channel %q cannot open %s: %w", c.cfg.Name, cfg.Port, err)
	}
	r.port = port

	c.serial = r

	c.log.Info("reading a serial port",
		"channel", c.cfg.Name, "port", cfg.Port, "baud", cfg.Baud,
		"framing", fr.Describe(), "parity", cfg.Parity, "flow", cfg.FlowControl,
		"quiet_after", cfg.QuietAfter)

	r.wg.Add(1)
	go r.loop()

	if cfg.QuietAfter > 0 {
		r.wg.Add(1)
		go r.watchForSilence()
	}

	return nil
}

// stopSerialSource closes the port.
func (c *Channel) stopSerialSource() error {
	if c.serial == nil {
		return nil
	}
	err := c.serial.close()
	c.serial = nil
	return err
}

// Stats returns a copy.
func (r *serialReader) Stats() SerialStats {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.stats
	s.Open = r.port != nil
	return s
}

// open configures and opens the port.
func (r *serialReader) open() (serialport.Port, error) {
	mode := &serialport.Mode{
		BaudRate: r.cfg.Baud,
		DataBits: r.cfg.DataBits,
	}

	switch r.cfg.Parity {
	case config.ParityOdd:
		mode.Parity = serialport.OddParity
	case config.ParityEven:
		mode.Parity = serialport.EvenParity
	case config.ParityMark:
		mode.Parity = serialport.MarkParity
	case config.ParitySpace:
		mode.Parity = serialport.SpaceParity
	default:
		mode.Parity = serialport.NoParity
	}

	switch r.cfg.StopBits {
	case "1.5":
		mode.StopBits = serialport.OnePointFiveStopBits
	case "2":
		mode.StopBits = serialport.TwoStopBits
	default:
		mode.StopBits = serialport.OneStopBit
	}

	port, err := serialport.Open(r.cfg.Port, mode)
	if err != nil {
		return nil, err
	}

	// Flow control after opening, because the library sets it separately from the mode. A failure here is reported rather
	// than ignored: with hardware flow control expected and not set, a device stops partway through a long message and
	// the result is a truncated message rather than an error.
	switch r.cfg.FlowControl {
	case config.FlowHardware:
		if err := port.SetRTS(true); err != nil {
			_ = port.Close()
			return nil, fmt.Errorf("enabling hardware flow control on %s: %w", r.cfg.Port, err)
		}
		if err := port.SetDTR(true); err != nil {
			_ = port.Close()
			return nil, fmt.Errorf("raising DTR on %s: %w", r.cfg.Port, err)
		}
	case config.FlowSoftware:
		// XON/XOFF is handled in the byte stream by the device and the driver. Nothing to set here, and saying so is
		// better than an empty case somebody later reads as an omission.
	}

	// No read timeout. A serial feed is silent for long stretches by design, and a timeout would turn every quiet
	// afternoon into a stream of errors. Silence is detected by quiet_after instead, which reports it as what it is.
	if err := port.SetReadTimeout(serialport.NoTimeout); err != nil {
		_ = port.Close()
		return nil, fmt.Errorf("configuring %s: %w", r.cfg.Port, err)
	}

	return port, nil
}

func (r *serialReader) close() error {
	r.closed.Do(func() {
		close(r.stop)

		// Closed while holding the lock so a concurrent reopen cannot replace it after this ran, which would leak the
		// new handle and keep the goroutine alive.
		r.mu.Lock()
		if r.port != nil {
			_ = r.port.Close()
			r.port = nil
		}
		r.mu.Unlock()
	})
	r.wg.Wait()
	return nil
}

// loop reads messages, reopening the port when it fails.
func (r *serialReader) loop() {
	defer r.wg.Done()

	for {
		select {
		case <-r.stop:
			return
		default:
		}

		r.mu.Lock()
		port := r.port
		r.mu.Unlock()

		if port == nil {
			if !r.waitBeforeReopen() {
				return
			}
			r.reopen()
			continue
		}

		if err := r.readFrom(port); err != nil {
			select {
			case <-r.stop:
				return
			default:
			}

			r.mu.Lock()
			r.stats.LastError = err.Error()
			if r.port != nil {
				_ = r.port.Close()
				r.port = nil
			}
			r.mu.Unlock()

			r.log.Error("the serial port failed and will be reopened",
				"channel", r.ch.cfg.Name, "port", r.cfg.Port, "err", err,
				"why", "a USB serial adapter unplugged and replugged returns the same device path with a "+
					"different kernel handle, so the old one errors forever and reopening is the only recovery")
		}
	}
}

// waitBeforeReopen sleeps, returning false if the channel is stopping.
func (r *serialReader) waitBeforeReopen() bool {
	select {
	case <-r.stop:
		return false
	case <-time.After(r.cfg.ReopenAfter):
		return true
	}
}

// reopen tries to open the port again.
func (r *serialReader) reopen() {
	port, err := r.open()
	if err != nil {
		r.mu.Lock()
		r.stats.LastError = err.Error()
		r.mu.Unlock()
		// Logged at debug, not error. A device that is switched off overnight would otherwise produce an error line every
		// few seconds until morning, which buries anything real.
		r.log.Debug("could not reopen the serial port",
			"channel", r.ch.cfg.Name, "port", r.cfg.Port, "err", err)
		return
	}

	r.mu.Lock()
	// Checked because close may have run while open was in progress. Without this the new handle leaks and the loop keeps
	// reading a port nobody can close.
	select {
	case <-r.stop:
		r.mu.Unlock()
		_ = port.Close()
		return
	default:
	}
	r.port = port
	r.stats.Reopens++
	r.mu.Unlock()

	r.log.Info("reopened the serial port", "channel", r.ch.cfg.Name, "port", r.cfg.Port)
}

// readFrom reads messages until the port fails.
func (r *serialReader) readFrom(port serialport.Port) error {
	reader := framing.NewReader(port, r.fr)

	for {
		select {
		case <-r.stop:
			return nil
		default:
		}

		msg, err := reader.ReadMessage()
		if err == io.EOF {
			// A serial port does not normally reach EOF. When it does, the device is gone.
			return errors.New("the port reported end of file, which for a serial line means the device or its " +
				"adapter has gone")
		}
		if err != nil {
			// An oversize message means the reader is mid-message with no way to find the next boundary. On a socket the
			// answer is to close and let the peer reconnect; here there is nothing to reconnect, so the port is reopened,
			// which discards whatever was in flight and resynchronises.
			if errors.Is(err, framing.ErrTooLarge) {
				r.mu.Lock()
				r.stats.FramingErrors++
				r.mu.Unlock()
				return fmt.Errorf("%w. Reopening the port to resynchronise, because there is no way to find where "+
					"the next message starts. If this repeats, the baud rate or the framing is wrong rather than "+
					"the device", err)
			}
			return err
		}

		now := time.Now()
		r.mu.Lock()
		r.stats.Messages++
		r.stats.LastMessage = now
		r.stats.Quiet = false
		r.lastRx = now
		r.mu.Unlock()

		if _, err := r.ch.handle(context.Background(), msg); err != nil {
			r.log.Error("a message from the serial port could not be processed",
				"channel", r.ch.cfg.Name, "err", err)
			// Not fatal. The next message may be fine, and reopening the port for one bad message would discard whatever
			// the device is sending now.
		}

		if err := r.reply(port); err != nil {
			return fmt.Errorf("sending the reply: %w", err)
		}
	}
}

// reply sends whatever the configuration says to send back.
func (r *serialReader) reply(port serialport.Port) error {
	switch r.cfg.Reply {
	case config.ReplyNone, "":
		return nil
	case config.ReplyACK:
		_, err := port.Write([]byte{0x06})
		return err
	case config.ReplyText:
		body, err := r.cfg.ReplyBytes()
		if err != nil {
			return err
		}
		_, err = port.Write(body)
		return err
	default:
		return fmt.Errorf("reply %q is not understood", r.cfg.Reply)
	}
}

// watchForSilence reports a feed that has gone quiet.
//
// The only way anybody learns a serial feed has died. Warned once per quiet_after rather than continuously, and cleared as
// soon as a message arrives, so the log says how long it has been quiet without repeating every second.
func (r *serialReader) watchForSilence() {
	defer r.wg.Done()

	// A quarter of the window, so silence is noticed reasonably promptly without checking constantly.
	interval := r.cfg.QuietAfter / 4
	if interval < time.Second {
		interval = time.Second
	}

	t := time.NewTicker(interval)
	defer t.Stop()

	// Counted from startup rather than from zero, so a channel started at three in the morning does not immediately
	// report itself quiet since the epoch.
	r.mu.Lock()
	r.lastRx = time.Now()
	r.mu.Unlock()

	warned := false

	for {
		select {
		case <-r.stop:
			return
		case <-t.C:
			r.mu.Lock()
			since := time.Since(r.lastRx)
			quiet := since >= r.cfg.QuietAfter
			r.stats.Quiet = quiet
			r.mu.Unlock()

			switch {
			case quiet && !warned:
				warned = true
				r.log.Warn("this serial feed has gone quiet",
					"channel", r.ch.cfg.Name, "port", r.cfg.Port,
					"silent_for", since.Round(time.Second),
					"expected_within", r.cfg.QuietAfter,
					"why", "a serial line has no connection to lose, so an unplugged cable, a device switched "+
						"off and a device with nothing to say are the same silence")
			case !quiet && warned:
				warned = false
				r.log.Info("the serial feed is sending again",
					"channel", r.ch.cfg.Name, "port", r.cfg.Port)
			}
		}
	}
}
