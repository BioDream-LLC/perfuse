// Package script runs Mirth Connect JavaScript.
//
// The goal is that a channel's scripts move across untouched. That is a
// compatibility exercise, not a design exercise, so the shape of everything here
// is dictated by what Mirth exposes: msg and tmp as E4X trees, six maps with
// Java's Map methods, the $ family of lookup functions, logger, and a handful of
// utility objects whose method names and date-format strings are Java's.
//
// Three decisions are ours rather than Mirth's, and each is a deliberate
// departure:
//
// A script runs with a deadline and is interrupted when it passes. Mirth will let
// a script loop forever, which takes the channel with it; an interface that stops
// accepting admissions because of a typo in a transformer is not acceptable, so
// there is always a timeout.
//
// Java interoperability is refused with an error naming the class, rather than
// returning undefined. A script reaching into Packages.java.sql cannot be made to
// work here, and the useful outcome is a message saying exactly that at the line
// where it happens, not a NullPointerException three lines later.
//
// File and database access are off unless a channel turns them on. Mirth grants
// both to every script by default. A transformer that can read any file the
// process can read is a reasonable thing to want and an unreasonable default.
package script

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/perfuse/internal/e4x"
	"github.com/biodream-llc/perfuse/internal/hl7xml"
	"github.com/biodream-llc/perfuse/internal/xtree"
	"github.com/dop251/goja"
	lua "github.com/yuin/gopher-lua"
)

// Permission names a capability a script may be granted.
type Permission string

const (
	// PermFile allows FileUtil to read and write files, within the directories named by Options.FileRoots.
	//
	// The confinement is not optional. Granting this alone used to mean the entire filesystem, which made an
	// editor-authored script able to read Perfuse's own database - password hashes, the LDAP service account
	// password, the OIDC client secret - as soon as an administrator started the channel.
	PermFile Permission = "file"
	// PermDatabase allows DatabaseConnectionFactory.
	PermDatabase Permission = "database"
	// PermRoute allows router.routeMessage to send to another channel.
	PermRoute Permission = "route"
)

// Options configures the engine.
type Options struct {
	// Timeout bounds a single execution. Zero means five seconds.
	Timeout time.Duration

	// Permissions granted to every script this engine runs.
	Permissions []Permission

	// FileRoots confines FileUtil to a set of directories.
	//
	// Required whenever PermFile is granted. Nil with PermFile granted means FileUtil refuses every call rather
	// than reaching the whole filesystem, which is what it used to do.
	FileRoots *FileRoots

	// MaxVMs bounds the pooled runtimes. Zero means eight.
	MaxVMs int

	// Language is which runtime the library belongs to.
	//
	// Needed because the library is compiled once at construction and a runtime cannot compile another runtime's source. Before
	// this field existed the library was always compiled as JavaScript, so a Lua library recorded a JavaScript syntax error that
	// only the JavaScript path ever looked at - and the Lua path, which had no library support at all, ignored both the error and
	// the library.
	//
	// Defaults to JavaScript, which is the zero value and the language every existing caller meant.
	Language Language

	// Library is source made available to every script this engine runs.
	//
	// This is Mirth's code templates. A site with forty channels usually has one library every channel
	// calls, and without this a migration means copying it into forty files - which is not only work
	// but forty places to fix the next time it changes.
	Library string

	// LibraryName is what the library is called in an error message. Defaults to a generic name.
	LibraryName string
}

// Engine compiles and runs scripts.
type Engine struct {
	opts Options

	// library is the compiled shared library, run once per runtime. One of these is set, never both, because Options.Language
	// decides which runtime the source belongs to.
	library    *goja.Program
	luaLibrary *lua.FunctionProto
	libraryErr error

	// Runtimes are pooled because creating one and installing the whole Mirth
	// environment costs far more than running a typical transformer, and an
	// interface engine runs one per message.
	pool chan *instance

	globalMap *SharedMap
	channels  sync.Map // channel name -> *SharedMap for globalChannelMap

	// route is how router.routeMessage reaches the engine. It is nil until the
	// runtime supplies it, and calling it then reports that plainly.
	route atomic[RouteFunc]
}

// RouteFunc sends a message to another channel by name.
type RouteFunc func(channel string, message []byte) error

// atomic is a tiny helper so the engine can be handed its router after
// construction without a mutex on the hot path.
type atomic[T any] struct {
	mu sync.RWMutex
	v  T
	ok bool
}

func (a *atomic[T]) set(v T) {
	a.mu.Lock()
	a.v, a.ok = v, true
	a.mu.Unlock()
}

func (a *atomic[T]) get() (T, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.v, a.ok
}

// New creates an engine.
func New(opts Options) *Engine {
	if opts.Timeout <= 0 {
		opts.Timeout = 5 * time.Second
	}
	if opts.MaxVMs <= 0 {
		opts.MaxVMs = 8
	}
	e := &Engine{
		opts:      opts,
		pool:      make(chan *instance, opts.MaxVMs),
		globalMap: NewSharedMap(),
	}

	// Compiled once here rather than per runtime. New cannot return an error without changing every
	// caller, so a bad library is remembered and reported when a runtime is created - which is before
	// any script runs, so nothing executes against a half-installed environment.
	if src := strings.TrimSpace(opts.Library); src != "" {
		name := opts.LibraryName
		if name == "" {
			name = "the shared script library"
		}

		if opts.Language == Lua {
			// Compiled to a proto here rather than parsed on every run. Protos are immutable and shareable, so one compile
			// serves every state - which matters because Lua gets a fresh state per message rather than a pooled one.
			proto, lerr := compileLuaLibrary(src)
			if lerr != nil {
				e.libraryErr = fmt.Errorf("%s: %w", name, lerr)

				return e
			}
			e.luaLibrary = proto

			return e
		}

		// Translated like any other script, because a library ported from Mirth code templates has the
		// same E4X in it as the channels that call it.
		rewritten, _, err := e4x.Preprocess(src)
		if err != nil {
			e.libraryErr = fmt.Errorf("%s: %w", name, err)
			return e
		}

		program, err := goja.Compile(name, rewritten, false)
		if err != nil {
			e.libraryErr = fmt.Errorf("%s: %w", name, cleanupError(err))
			return e
		}
		e.library = program
	}

	return e
}

// SetRouter supplies the function router.routeMessage calls.
func (e *Engine) SetRouter(fn RouteFunc) { e.route.set(fn) }

func (e *Engine) allowed(p Permission) bool {
	for _, have := range e.opts.Permissions {
		if have == p {
			return true
		}
	}
	return false
}

// Script is a compiled script.
type Script struct {
	Name string

	// Source is what the author wrote, kept so the interface can show it.
	Source string

	// Rewritten is what actually runs, kept because a stack trace refers to it.
	Rewritten string

	// Notes record the E4X constructs that were translated, so the interface can
	// say what happened to somebody's script instead of pretending nothing did.
	Notes []e4x.Note

	// language decides which runtime executes it. Zero value is JavaScript, so a Script built by older code without naming a
	// language behaves as it always did.
	language Language

	// compiled is set for languages other than JavaScript, which uses program.
	compiled compiled

	program *goja.Program
	kind    Kind
}

// Language reports which language the script is written in.
func (s *Script) Language() Language {
	if s == nil || s.language == "" {
		return DefaultLanguage
	}

	return s.language
}

// Kind distinguishes the wrapper a script gets.
type Kind int

const (
	// Filter must produce a boolean. Mirth requires a return statement.
	Filter Kind = iota
	// Transformer mutates the message and returns nothing.
	Transformer

	// Preprocessor runs before the message is parsed and returns the text to parse.
	//
	// It exists because a preprocessor is how sites repair messages that do not parse - a stray
	// character from a serial gateway, a segment terminator that arrived as a line feed, a field a
	// vendor pads with something that is not legal in the position they put it. Without it those
	// messages are simply rejected, and Perfuse would be unable to accept traffic that Mirth accepts
	// today. Its input is text rather than a tree for exactly that reason: there may be no tree.
	Preprocessor

	// Postprocessor runs after the message has been handled and cannot change it.
	//
	// The message and its outcome are visible, so it can notify, count or record. It cannot alter
	// what was sent, because by the time it runs the sending has already happened, and a script that
	// appeared to change a delivered message would be lying.
	Postprocessor

	// Writer is a destination script: it handles the message itself instead of something being sent.
	//
	// It needs both halves that no other kind has together - the message as a tree, because it is doing
	// a transformer's kind of work, and its return value captured, because it is reporting whether it
	// succeeded. Compiling one as a Transformer silently discards what it returned, which is how a
	// destination that refused a message reported success on the first attempt at this.
	Writer

	// Lifecycle runs once at start or stop, with no message in scope at all.
	//
	// Separate from Postprocessor because the difference is visible to the author: there is no
	// message, so a script that reaches for one should be told there is none rather than handed an
	// empty one and left to wonder why its lookup found nothing.
	Lifecycle

	// Reader is a JavaScript Reader source: a script that produces messages from nothing.
	//
	// It must return a string (one message) or an array of strings (many messages). Returning
	// nothing or an empty array means the poll produced no messages. Like Lifecycle, there is no
	// inbound message in scope.
	Reader
)

// Compile prepares a script.
//
// The user's code is wrapped in a function so that a bare "return" is legal,
// which is how every Mirth filter is written. Wrapping is done on one line so
// that reported line numbers still match what the author sees.
func (e *Engine) Compile(name, source string, kind Kind) (*Script, error) {
	return e.CompileIn(name, source, kind, DefaultLanguage)
}

// Release frees anything a compiled script holds.
//
// Only WebAssembly holds anything: goja and gopher-lua state is ordinary garbage, while a wazero module is mapped executable memory
// the collector does not account for. Expressed as an interface check rather than a language check so that a runtime added later
// cannot leak by omission - a compiled form that needs cleaning up says so, and this finds it.
//
// Safe on a nil script and on one that holds nothing, because every caller is a shutdown path and a channel that failed to start
// should not fail differently while stopping.
func (s *Script) Release() error {
	if s == nil || s.compiled == nil {
		return nil
	}

	c, ok := s.compiled.(interface{ Close() error })
	if !ok {
		return nil
	}

	return c.Close()
}

// CompileIn compiles a script in a named language.
//
// Compile is kept as the JavaScript-defaulting form rather than being changed to take a language, because every existing caller
// means JavaScript and changing the signature would make each of them assert something it had not thought about.
func (e *Engine) CompileIn(name, source string, kind Kind, language Language) (*Script, error) {
	// A library that would not compile fails here, before any script is accepted.
	//
	// The JavaScript path also reports this when a runtime is created, which is where it used to be reported first. That was late
	// enough to be worth moving: a channel whose shared library does not parse should refuse to load rather than load and then
	// fail every message, and refusing at compile time is what makes perfuse check catch it.
	//
	// It was worse than late for Lua. New recorded the error and nothing on the Lua path read it, so a library with a syntax error
	// was accepted and the script ran without it - the fault reappearing as a nil global on the first message.
	if e.libraryErr != nil {
		return nil, e.libraryErr
	}

	if language == WASM {
		c, err := wasmRuntime{}.compile(name, source, kind)
		if err != nil {
			return nil, err
		}

		return &Script{
			Name:     name,
			Source:   source,
			language: WASM,
			compiled: c,
			kind:     kind,
		}, nil
	}

	if language == Lua {
		c, err := luaRuntime{}.compile(name, source, kind)
		if err != nil {
			return nil, err
		}

		return &Script{
			Name:     name,
			Source:   source,
			language: Lua,
			compiled: c,
			kind:     kind,
		}, nil
	}

	rewritten, notes, err := e4x.Preprocess(source)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}

	var wrapped string
	switch kind {
	case Filter:
		wrapped = "(function(){" + rewritten + "\n})()"
	default:
		wrapped = "(function(){" + rewritten + "\n})()"
	}

	program, err := goja.Compile(name, wrapped, false)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, cleanupError(err))
	}

	return &Script{
		Name:      name,
		Source:    source,
		Rewritten: rewritten,
		Notes:     notes,
		language:  JavaScript,
		program:   program,
		kind:      kind,
	}, nil
}

// Context is everything a script can see for one message.
type Context struct {
	// Message is bound to msg: the inbound message as a tree. Nil for a preprocessor, which runs
	// before parsing and may be looking at something that does not parse at all.
	Message *xtree.Node

	// Raw is bound to message: the message as text. This is what a preprocessor reads and returns.
	Raw string

	// RawUnavailable says there is no message text to offer, which is not the same as a message whose text is empty.
	//
	// The path-addressed formats set this. Their scripts reach values through msg.get and msg.set, and this stage does no
	// re-serialisation on purpose - each format's handler owns its own encoding, and a stage that re-encoded would have to
	// learn three of them. So by the time a transformer runs, the declarative steps have already changed the message and the
	// bytes that arrived no longer describe it.
	//
	// Given that, the choice is between binding text that is stale and binding nothing. Stale is worse: it would give a script
	// two disagreeing views of one message, where msg.get returns the current value and message shows the old one, and
	// nothing would report the disagreement. Binding it at the filter and withholding it at the transformer would be accurate
	// but would make the same identifier mean different things two lines apart.
	//
	// So it is withheld throughout, and withheld rather than emptied. An empty string is the failure this project has hit
	// repeatedly: a script computes from nothing, writes a plausible answer and reports success. An absent identifier fails on
	// the first line that touches it, which is the outcome an author can act on. Raw text work belongs in a preprocessor,
	// which runs before any of this and does receive it.
	RawUnavailable bool

	// Output is bound to tmp. When nil, tmp is the same tree as msg, which is
	// what Mirth does for a channel with no outbound template and is how most
	// transformers are written.
	Output *xtree.Node

	ChannelName  string
	ChannelMap   *SharedMap
	ConnectorMap *SharedMap
	ResponseMap  *SharedMap
	SourceMap    *SharedMap

	// Log receives logger calls. When nil they are discarded.
	Log func(level, message string)
}

func (c *Context) log(level, message string) {
	if c.Log != nil {
		c.Log(level, message)
	}
}

// Result reports what a script did.
type Result struct {
	// Accept is the filter verdict. It is false for a transformer.
	Accept bool

	// Logs are the logger lines the script produced, in order.
	Logs []LogLine

	// Duration is how long the script ran.
	Duration time.Duration

	// Text is what a preprocessor returned, when it returned anything.
	Text string

	// Replaced says Text is meaningful. Without it an empty string returned deliberately could not be
	// told apart from a script that returned nothing, and one of those means "discard the message".
	Replaced bool

	// Messages is what a Reader script returned: zero or more raw messages to feed into the channel.
	Messages []string
}

// LogLine is one logger call.
type LogLine struct {
	Level   string
	Message string
}

// Run executes a script.
func (e *Engine) Run(s *Script, ctx *Context) (Result, error) {
	if s == nil {
		return Result{Accept: true}, nil
	}

	// Dispatched here rather than at each call site, so that everything after this point - the timeout, the log collection, the
	// duration, what a filter verdict means - is shared. Two engines would have meant two answers to each of those, and a
	// filter written in Lua has to mean the same thing as the same filter written in JavaScript.
	if s.language == Lua || s.language == WASM {
		started := time.Now()
		var logs []LogLine

		// The log function is wrapped rather than passed through, because the duration and the log list belong to the engine.
		// A language that collected its own would be free to report a duration excluding its own setup.
		inner := ctx.Log
		ctx.Log = func(level, message string) {
			logs = append(logs, LogLine{Level: level, Message: message})
			if inner != nil {
				inner(level, message)
			}
		}
		defer func() { ctx.Log = inner }()

		res, err := s.compiled.run(e, ctx, s.kind)
		res.Logs = logs
		res.Duration = time.Since(started)

		return res, err
	}

	return e.runJavaScriptWith(s, ctx, nil)
}

// runJavaScriptWith is Run's JavaScript half with a chance to install extra globals.
//
// The hook exists for the same reason runWith exists on the Lua side: a format addressed by path replaces msg with its own
// binding, and doing that by duplicating the run path would mean two answers to the timeout, the log bound, the interrupt
// handling and every verdict rule. Those are engine semantics, not language semantics, and a filter written in one language has
// to mean what the same filter means in the other.
func (e *Engine) runJavaScriptWith(s *Script, ctx *Context, extra func(*goja.Runtime) error) (Result, error) {
	inst, err := e.acquire()
	if err != nil {
		return Result{}, err
	}
	defer e.release(inst)

	var logs []LogLine
	captured := ctx.Log
	ctx.Log = func(level, message string) {
		// Bound the log so a script looping over a logger call cannot exhaust
		// memory before the timeout fires.
		if len(logs) < 500 {
			logs = append(logs, LogLine{Level: level, Message: message})
		}
		if captured != nil {
			captured(level, message)
		}
	}

	if err := inst.bind(ctx); err != nil {
		return Result{}, err
	}

	// After bind, so a path binding replaces the msg that bind installed for the tree formats rather than being overwritten by
	// it. Before the program runs, so the script never sees a half-installed environment.
	if extra != nil {
		if err := extra(inst.vm); err != nil {
			return Result{}, err
		}
	}

	// The deadline is enforced by interrupting the VM. A script that ignores it
	// cannot, because the interrupt is checked by the interpreter itself.
	timer := time.AfterFunc(e.opts.Timeout, func() {
		inst.vm.Interrupt(errTimeout)
	})

	start := time.Now()
	value, err := inst.vm.RunProgram(s.program)
	elapsed := time.Since(start)

	timer.Stop()
	inst.vm.ClearInterrupt()
	inst.unbind()

	result := Result{Logs: logs, Duration: elapsed}

	if err != nil {
		var interrupted *goja.InterruptedError
		if errors.As(err, &interrupted) {
			if interrupted.Value() == errTimeout {
				return result, fmt.Errorf("%s: still running after %s, stopped", s.Name, e.opts.Timeout)
			}
		}
		return result, fmt.Errorf("%s: %w", s.Name, cleanupError(err))
	}

	switch s.kind {
	case Filter:
		result.Accept = truthy(value)

	case Writer:
		result.Accept = true
		// The return value is captured for the same reason as a preprocessor's, and absence means the
		// same thing: the script did its work and fell off the end, which is how nearly every Mirth
		// JavaScript Writer is written.
		if value != nil && !goja.IsUndefined(value) && !goja.IsNull(value) {
			result.Text = value.String()
			result.Replaced = true
		}

	case Preprocessor:
		result.Accept = true
		// A preprocessor that returns nothing has left the message alone. That is the common case -
		// most preprocessors modify the bound variable and fall off the end - and treating a missing
		// return as "the message is now empty" would discard traffic on a technicality.
		if value != nil && !goja.IsUndefined(value) && !goja.IsNull(value) {
			result.Text = value.String()
			result.Replaced = true
		}

	case Reader:
		result.Accept = true
		if value != nil && !goja.IsUndefined(value) && !goja.IsNull(value) {
			obj := value.Export()
			switch v := obj.(type) {
			case string:
				if v != "" {
					result.Messages = []string{v}
				}
			case []interface{}:
				for _, item := range v {
					if s, ok := item.(string); ok && s != "" {
						result.Messages = append(result.Messages, s)
					}
				}
			default:
				// Coerce to string if the value is something else.
				s := value.String()
				if s != "" && s != "undefined" && s != "null" {
					result.Messages = []string{s}
				}
			}
		}

	default:
		result.Accept = true
	}
	return result, nil
}

// ErrTimeout marks a script that ran past its deadline.
//
// Exported so the engine can count timeouts separately from ordinary errors. A
// script that is timing out is a different operational problem from one that
// throws: the first is usually load or an infinite loop and affects everything
// behind it, the second is usually one message.
var ErrTimeout = errors.New("script timeout")

// errTimeout is retained for the existing internal uses.
var errTimeout = ErrTimeout

// truthy applies Mirth's filter rule.
//
// A filter that returns nothing at all is treated as accepting, because that is
// what Mirth does and because the alternative silently discards every message
// through a channel whose author forgot a return statement. Dropping clinical
// data quietly is the worse failure.
func truthy(v goja.Value) bool {
	if v == nil || goja.IsUndefined(v) || goja.IsNull(v) {
		return true
	}
	if x, ok := v.(*goja.Object); ok {
		// An E4X value is truthy when it has content, which is how
		// "return msg['PID']['PID.3']['PID.3.1']" behaves in Rhino.
		if s := x.String(); s != "" {
			return s != "false"
		}
		return false
	}
	return v.ToBoolean()
}

func (e *Engine) acquire() (*instance, error) {
	select {
	case inst := <-e.pool:
		return inst, nil
	default:
	}
	return e.newInstance()
}

func (e *Engine) release(inst *instance) {
	select {
	case e.pool <- inst:
	default:
		// The pool is full; let this one go.
	}
}

// cleanupError turns goja's error text into something an integration analyst can
// act on, chiefly by stripping the wrapper function this package added.
func cleanupError(err error) error {
	msg := err.Error()
	msg = strings.ReplaceAll(msg, "(anonymous)", "script")
	if ex, ok := err.(*goja.Exception); ok {
		if v := ex.Value(); v != nil {
			if obj, ok := v.(*goja.Object); ok {
				if stack := obj.Get("stack"); stack != nil && !goja.IsUndefined(stack) {
					return fmt.Errorf("%s", strings.SplitN(stack.String(), "\n\tat ", 2)[0])
				}
			}
		}
	}
	return errors.New(msg)
}

// er7 converts a tree back to HL7 for the helpers that need the raw message.
func er7(node *xtree.Node) string {
	if node == nil {
		return ""
	}
	out, err := hl7xml.ToER7(node, hl7xml.DefaultOptions())
	if err != nil {
		return ""
	}
	return string(out)
}
