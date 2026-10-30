//go:build linux

package main

// nsall is a combined Nintendo Switch controller daemon that eliminates the
// text-serialization IPC overhead between nsfrontend and nsbackend.
//
// # Modes
//
// ## Standalone (default — joystick and Switch both on the Pi)
//
//	nsall [-j /dev/input/js0] [-f /dev/hidg0]
//
// Opens the local joystick and drives /dev/hidg0 directly. Zero IPC, minimum
// possible latency.
//
// ## Pipe / SSH (joystick on host PC, Switch on Pi)
//
//	# Host PC:
//	nsall --client [-j /dev/input/js0] | ssh pi@PI nsall --server
//
// --client reads the local joystick and writes 17-byte binary Event frames to
// stdout.  --server reads those frames from stdin and drives /dev/hidg0.
// This replaces the text-command SSH pipe used by nsfrontend + nsbackend.
//
// ## Direct TCP (LAN, no SSH port-forwarding needed)
//
//	# Pi:
//	nsall --serve :12345
//
//	# Host PC:
//	nsall --connect PI_IP:12345 [-j /dev/input/js0]
//
// Binary frames are sent directly over a TCP connection.
// Unix sockets are also supported: --serve unix:/tmp/nsall.sock

import (
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/Johny-Butcher/better-raspberry-switch-control/nscontroller"
	"github.com/Johny-Butcher/better-raspberry-switch-control/nscontroller/js"
	"github.com/omakoto/go-common/src/common"
	"github.com/omakoto/go-common/src/fileutils"
	"github.com/pborman/getopt/v2"
)

// ── Flags ────────────────────────────────────────────────────────────────────

var (
	debugFlag   = getopt.BoolLong("debug", 'd', "Enable debug output")
	joystickDev = getopt.StringLong("joystick", 'j', "/dev/input/js0", "Joystick device path (client/standalone modes)")
	hidDev      = getopt.StringLong("dev", 'f', "/dev/hidg0", "HID gadget device path (server/standalone modes)")
	rateLimit   = getopt.IntLong("rate-limit", 'r', 120, "Joystick axis rate-limit in Hz (0 = unlimited)")
	usbTickMs   = getopt.IntLong("tick-interval-millis", 0, 5, "USB HID report interval in milliseconds")

	// Mode selectors (mutually exclusive; default is standalone).
	serverMode  = getopt.BoolLong("server", 0, "Server mode: read binary frames from stdin, drive HID gadget")
	clientMode  = getopt.BoolLong("client", 0, "Client mode: read joystick, write binary frames to stdout")
	serveAddr   = getopt.StringLong("serve", 0, "", `TCP/Unix listen address, e.g. ":12345" or "unix:/tmp/nsall.sock"`)
	connectAddr = getopt.StringLong("connect", 0, "", `TCP/Unix dial address, e.g. "pi:12345" or "unix:/tmp/nsall.sock"`)
)

// ── Dispatcher selection ─────────────────────────────────────────────────────

func mustGetDispatcher(j *js.Js) nscontroller.JoystickDispatcher {
	switch {
	case strings.Contains(j.Name, "X-Box One") || strings.Contains(j.Name, "Xbox"):
		return nscontroller.XBoxOneJoystickDispatcher
	case strings.Contains(j.Name, "Nintendo Switch Pro Controller"):
		return nscontroller.NSProJoystickDispatcher
	case strings.Contains(j.Name, "Sony Interactive Entertainment Wireless Controller"),
		strings.Contains(j.Name, "Sony Computer Entertainment Wireless Controller"):
		return nscontroller.PsJoystickDispatcher
	default:
		common.Fatalf("Unsupported joystick: %q", j.Name)
		return nil
	}
}

// ── Joystick input pipeline ───────────────────────────────────────────────────

// inputPipeline holds the workers that make up the joystick → consumer chain.
type inputPipeline struct {
	joystick    *nscontroller.JoystickInput
	rateLimiter *nscontroller.RateLimiter
	autoFirer   *nscontroller.AutoFirer
	stdinProxy  *nscontroller.StreamInput
}

// newInputPipeline builds:
//
//	JoystickInput → RateLimiter → AutoFirer → consumer
//
// and a StreamInput that forwards stdin text commands directly to consumer
// (bypassing rate-limiting and autofire, matching nsfrontend behaviour).
func newInputPipeline(consumer nscontroller.Consumer) (*inputPipeline, error) {
	j, err := js.NewJs(*joystickDev)
	if err != nil {
		return nil, fmt.Errorf("open joystick %s: %w", *joystickDev, err)
	}

	autoFirer := nscontroller.NewAutoFirer(consumer)
	rateLimiter := nscontroller.NewRateLimiter(*rateLimit, autoFirer.Consume)

	joystickInput, err := nscontroller.NewJoystickInput(j, mustGetDispatcher(j), rateLimiter.Consume)
	if err != nil {
		j.Close()
		rateLimiter.Close()
		autoFirer.Close()
		return nil, fmt.Errorf("create joystick input: %w", err)
	}

	stdinProxy, err := nscontroller.NewStreamInput(os.Stdin, consumer)
	if err != nil {
		joystickInput.Close()
		rateLimiter.Close()
		autoFirer.Close()
		return nil, fmt.Errorf("create stdin proxy: %w", err)
	}

	return &inputPipeline{
		joystick:    joystickInput,
		rateLimiter: rateLimiter,
		autoFirer:   autoFirer,
		stdinProxy:  stdinProxy,
	}, nil
}

// run starts all goroutines in the pipeline.
func (p *inputPipeline) run() {
	p.autoFirer.Run()
	p.joystick.Run()
	p.stdinProxy.Run()
}

// close stops all workers. Call after waitClose.
func (p *inputPipeline) close() {
	p.stdinProxy.Close()
	p.joystick.Close()
	p.rateLimiter.Close()
	p.autoFirer.Close()
}

// waitClose blocks until stdin is closed (^D), which is the natural exit signal.
func (p *inputPipeline) waitClose() {
	p.stdinProxy.WaitClose()
}

// ── HID gadget helpers ────────────────────────────────────────────────────────

// newController creates a Controller and starts an async goroutine that waits
// for the device file to appear before calling Connect. This mirrors the
// asyncConnect logic in nsbackend so nsall works even if /dev/hidg0 appears
// after the process starts (e.g. after USB enumeration completes).
func newController() *nscontroller.Controller {
	con := nscontroller.NewController(*hidDev, time.Duration(*usbTickMs)*time.Millisecond)

	go func() {
		if !fileutils.FileExists(con.Path()) {
			fmt.Printf("nsall: waiting for device '%s'...\n", con.Path())
			for !fileutils.FileExists(con.Path()) {
				time.Sleep(100 * time.Millisecond)
			}
		}
		fmt.Printf("nsall: opening %s...\n", con.Path())
		if err := con.Connect(); err != nil {
			common.Fatalf("nsall: cannot connect to '%s': %v", con.Path(), err)
		}
		fmt.Printf("nsall: connected to %s\n", con.Path())
	}()

	return con
}

// ── Frame I/O ─────────────────────────────────────────────────────────────────

// frameConsumer returns a Consumer that encodes each Event as a binary frame
// and writes it to w. A write failure is fatal because lost frames = lost inputs.
func frameConsumer(w io.Writer) nscontroller.Consumer {
	return func(ev *nscontroller.Event) {
		if err := nscontroller.WriteFrame(w, ev); err != nil {
			// Only log (don't fatal) so the caller can decide whether to reconnect.
			common.Warnf("nsall: frame write error: %v", err)
		}
	}
}

// receiveFrames reads binary Event frames from r and feeds each one to consumer
// until the stream closes or an error occurs.
func receiveFrames(r io.Reader, consumer nscontroller.Consumer) error {
	for {
		ev, err := nscontroller.ReadFrame(r)
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return nil
		}
		if err != nil {
			return err
		}
		consumer(&ev)
	}
}

// ── Network helpers ───────────────────────────────────────────────────────────

// parseAddr splits "unix:/path" into ("unix", "/path") and anything else into
// ("tcp", addr) so both Unix-domain sockets and TCP are supported.
func parseAddr(addr string) (network, address string) {
	if strings.HasPrefix(addr, "unix:") {
		return "unix", strings.TrimPrefix(addr, "unix:")
	}
	return "tcp", addr
}

// ── Modes ─────────────────────────────────────────────────────────────────────

// runStandalone: joystick and Switch are both on the same machine (the Pi).
//
//	JoystickInput → RateLimiter → AutoFirer → DirectConsumer → Controller → /dev/hidg0
func runStandalone() int {
	con := newController()
	defer con.Close()

	direct := nscontroller.NewDirectConsumer(con)

	pipe, err := newInputPipeline(direct.Consume)
	common.Checke(err)
	defer pipe.close()

	pipe.run()

	fmt.Println("nsall (standalone): ready — joystick active, ^D to exit.")
	pipe.waitClose()
	return 0
}

// runServer: read binary frames from stdin, drive the HID gadget.
//
//	stdin (binary frames) → DirectConsumer → Controller → /dev/hidg0
//
// Pair with: nsall --client | ssh pi@PI nsall --server
func runServer() int {
	con := newController()
	defer con.Close()

	direct := nscontroller.NewDirectConsumer(con)

	fmt.Fprintln(os.Stderr, "nsall (server): reading binary frames from stdin...")
	if err := receiveFrames(os.Stdin, direct.Consume); err != nil {
		common.Fatalf("nsall (server): read error: %v", err)
	}
	fmt.Fprintln(os.Stderr, "nsall (server): stdin closed, exiting.")
	return 0
}

// runClient: read local joystick, write binary frames to stdout.
//
//	JoystickInput → RateLimiter → AutoFirer → frameConsumer(stdout)
//
// Status/debug lines go to stderr so they don't corrupt the binary stream.
// Pair with: nsall --client | ssh pi@PI nsall --server
func runClient() int {
	pipe, err := newInputPipeline(frameConsumer(os.Stdout))
	common.Checke(err)
	defer pipe.close()

	pipe.run()

	fmt.Fprintln(os.Stderr, "nsall (client): streaming binary frames — ^D to exit.")
	pipe.waitClose()
	return 0
}

// runServe: listen on a TCP/Unix address, handle one client at a time.
//
//	net.Conn (binary frames) → DirectConsumer → Controller → /dev/hidg0
func runServe(addr string) int {
	network, address := parseAddr(addr)

	ln, err := net.Listen(network, address)
	common.Checkf(err, "nsall (serve): listen on %s failed", addr)
	defer ln.Close()

	con := newController()
	defer con.Close()
	direct := nscontroller.NewDirectConsumer(con)

	fmt.Printf("nsall (serve): listening on %s %s\n", network, address)

	for {
		conn, err := ln.Accept()
		if err != nil {
			// ln.Close() from defer causes Accept to return; treat that as a clean exit.
			if strings.Contains(err.Error(), "use of closed network connection") {
				break
			}
			common.Warnf("nsall (serve): accept error: %v", err)
			continue
		}
		fmt.Printf("nsall (serve): client connected from %s\n", conn.RemoteAddr())
		if err := receiveFrames(conn, direct.Consume); err != nil {
			common.Warnf("nsall (serve): client error: %v", err)
		} else {
			fmt.Println("nsall (serve): client disconnected.")
		}
		conn.Close()
	}
	return 0
}

// runConnect: connect to a TCP/Unix server, stream joystick frames to it.
//
//	JoystickInput → RateLimiter → AutoFirer → frameConsumer(conn)
func runConnect(addr string) int {
	network, address := parseAddr(addr)

	conn, err := net.Dial(network, address)
	common.Checkf(err, "nsall (connect): cannot connect to %s", addr)
	defer conn.Close()

	fmt.Printf("nsall (connect): connected to %s %s — streaming frames.\n", network, address)

	pipe, err := newInputPipeline(frameConsumer(conn))
	common.Checke(err)
	defer pipe.close()

	pipe.run()

	fmt.Fprintln(os.Stderr, "nsall (connect): ^D to exit.")
	pipe.waitClose()
	return 0
}

// ── Entry point ───────────────────────────────────────────────────────────────

func realMain() int {
	getopt.Parse()

	if *debugFlag {
		common.DebugEnabled = true
		common.VerboseEnabled = true
	}

	// Exactly one mode is active; the first matching case wins.
	switch {
	case *connectAddr != "":
		return runConnect(*connectAddr)
	case *serveAddr != "":
		return runServe(*serveAddr)
	case *clientMode:
		return runClient()
	case *serverMode:
		return runServer()
	default:
		return runStandalone()
	}
}

func main() {
	common.RunAndExit(realMain)
}
