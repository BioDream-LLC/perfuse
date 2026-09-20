package script

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/perfuse/internal/e4x"
	"github.com/biodream-llc/perfuse/internal/hl7xml"
	"github.com/biodream-llc/perfuse/internal/xtree"
	"github.com/dop251/goja"
)

// instance is one pooled runtime with the Mirth environment installed.
type instance struct {
	vm  *goja.Runtime
	e4x *e4x.Runtime
	eng *Engine
	ctx *Context

	// baselineKeys is the set of global property names present after the environment is
	// installed. Any globals added during a script execution are removed in unbind, so that one
	// message's (or one poll's) bare assignments cannot leak into the next invocation. Without
	// this a Reader script that writes `lastResult = x` on one poll sees it on the next, which
	// for a transformer means one patient's data visible while handling another's.
	baselineKeys map[string]struct{}
}

func (e *Engine) newInstance() (*instance, error) {
	// A library that would not compile is reported before anything runs, rather than being skipped.
	// Silently running without it would mean every script that calls into it fails with an
	// "undefined is not a function" that points at the wrong file.
	if e.libraryErr != nil {
		return nil, e.libraryErr
	}

	vm := goja.New()

	// Mirth's field names are Java's, so JavaScript's own field names have to be
	// left visible too; nothing is removed from the global object.
	rt, err := e4x.NewRuntime(vm)
	if err != nil {
		return nil, err
	}

	inst := &instance{vm: vm, e4x: rt, eng: e}
	if err := inst.install(); err != nil {
		return nil, err
	}

	// The shared library runs once per instance, at the top level, so whatever it declares is in scope
	// for every script this instance later runs. Running it here rather than prepending it to each
	// script means a library error is reported as a library error, and a reported line number in a
	// channel's own script still matches what its author sees.
	if e.library != nil {
		if _, err := vm.RunProgram(e.library); err != nil {
			return nil, fmt.Errorf("the shared script library failed to run: %w", cleanupError(err))
		}
	}

	// Snapshot the global keys after installation. Anything added during a script execution is
	// considered per-invocation state and will be removed in unbind.
	keys := vm.GlobalObject().Keys()
	inst.baselineKeys = make(map[string]struct{}, len(keys))
	for _, k := range keys {
		inst.baselineKeys[k] = struct{}{}
	}

	return inst, nil
}

// bind attaches one message's context to the runtime.
func (i *instance) bind(ctx *Context) error {
	i.ctx = ctx
	vm := i.vm

	output := ctx.Output
	if output == nil {
		// No outbound template: tmp and msg are the same tree, which is how a
		// transformer that edits the message in place is written.
		output = ctx.Message
	}

	// A preprocessor runs before parsing, so there may be no tree at all. msg and tmp are bound to
	// null in that case rather than skipped, because an unbound identifier throws a ReferenceError
	// while a null one can be tested, and a script that mentions msg by accident should get a
	// sensible complaint rather than a crash.
	if ctx.Message == nil {
		for _, name := range []string{"msg", "tmp"} {
			if err := vm.Set(name, goja.Null()); err != nil {
				return err
			}
		}
	} else {
		for name, value := range map[string]goja.Value{
			"msg": i.e4x.Wrap(ctx.Message),
			"tmp": i.e4x.Wrap(output),
		} {
			if err := vm.Set(name, value); err != nil {
				return err
			}
		}
	}

	// The raw text, under the name Mirth uses. A preprocessor reads and returns this.
	//
	// Left undefined when there is none, so a script reaching for it fails on that line rather than computing from an empty
	// string and reporting success. Set explicitly rather than skipped: these runtimes are pooled, and a skipped binding would
	// leave whatever the previous message put there.
	if ctx.RawUnavailable {
		if err := vm.Set("message", goja.Undefined()); err != nil {
			return err
		}
	} else if err := vm.Set("message", ctx.Raw); err != nil {
		return err
	}

	for name, m := range map[string]*SharedMap{
		"channelMap":       ctx.ChannelMap,
		"connectorMap":     ctx.ConnectorMap,
		"responseMap":      ctx.ResponseMap,
		"sourceMap":        ctx.SourceMap,
		"globalMap":        i.eng.globalMap,
		"globalChannelMap": i.eng.channelScope(ctx.ChannelName),
	} {
		if m == nil {
			m = NewSharedMap()
		}
		if err := vm.Set(name, i.wrapMap(m)); err != nil {
			return err
		}
	}

	return nil
}

// unbind clears per-message state so a pooled runtime cannot leak one patient's
// data into the next message. This is not a tidiness measure: a stale msg binding
// would be a cross-patient disclosure.
func (i *instance) unbind() {
	for _, name := range []string{
		"msg", "tmp", "message", "channelMap", "connectorMap", "responseMap", "sourceMap",
		"globalChannelMap",
	} {
		_ = i.vm.Set(name, goja.Undefined())
	}

	// Remove any globals the script created during this execution. A bare assignment
	// like `lastResult = x` (without var) creates a global property, and without this
	// cleanup it would be visible on the next invocation of a pooled VM - leaking one
	// message's data into the next.
	global := i.vm.GlobalObject()
	for _, key := range global.Keys() {
		if _, baseline := i.baselineKeys[key]; !baseline {
			_ = global.Delete(key)
		}
	}

	i.ctx = nil
}

func (e *Engine) channelScope(name string) *SharedMap {
	if name == "" {
		name = "(unnamed)"
	}
	if v, ok := e.channels.Load(name); ok {
		return v.(*SharedMap)
	}
	created := NewSharedMap()
	actual, _ := e.channels.LoadOrStore(name, created)
	return actual.(*SharedMap)
}

// install adds everything that does not change between messages.
func (i *instance) install() error {
	vm := i.vm

	if err := i.installLogger(); err != nil {
		return err
	}
	if err := i.installLookups(); err != nil {
		return err
	}
	if err := i.installUtilities(); err != nil {
		return err
	}
	if err := i.installSerializers(); err != nil {
		return err
	}
	if err := i.installFileUtil(); err != nil {
		return err
	}
	if err := i.installRouter(); err != nil {
		return err
	}
	if err := i.installJavaRefusal(); err != nil {
		return err
	}

	// validate(value, default, replacements) is Mirth's own helper and appears in
	// a great many generated transformers.
	return vm.Set("validate", func(call goja.FunctionCall) goja.Value {
		value := call.Argument(0)
		text := ""
		if !goja.IsUndefined(value) && !goja.IsNull(value) {
			text = value.String()
		}
		if strings.TrimSpace(text) == "" {
			fallback := call.Argument(1)
			if goja.IsUndefined(fallback) || goja.IsNull(fallback) {
				return vm.ToValue("")
			}
			return vm.ToValue(fallback.String())
		}

		// The third argument is a list of [pattern, replacement] pairs.
		if reps := call.Argument(2); !goja.IsUndefined(reps) && !goja.IsNull(reps) {
			if arr, ok := reps.(*goja.Object); ok {
				length := int(arr.Get("length").ToInteger())
				for n := 0; n < length; n++ {
					pair, ok := arr.Get(strconv.Itoa(n)).(*goja.Object)
					if !ok {
						continue
					}
					from := pair.Get("0")
					to := pair.Get("1")
					if from != nil && to != nil {
						text = strings.ReplaceAll(text, from.String(), to.String())
					}
				}
			}
		}
		return vm.ToValue(text)
	})
}

func (i *instance) installLogger() error {
	logger := i.vm.NewObject()
	for _, level := range []string{"trace", "debug", "info", "warn", "error", "fatal"} {
		lvl := level
		if err := logger.Set(lvl, func(call goja.FunctionCall) goja.Value {
			parts := make([]string, 0, len(call.Arguments))
			for _, a := range call.Arguments {
				parts = append(parts, a.String())
			}
			if i.ctx != nil {
				i.ctx.log(lvl, strings.Join(parts, " "))
			}
			return goja.Undefined()
		}); err != nil {
			return err
		}
	}
	return i.vm.Set("logger", logger)
}

// installLookups adds the $ family.
//
// Mirth's $('name') searches every scope. The order matters when the same key is
// in two of them, so it is the documented one: the narrowest scope wins, because
// a value set for this connector is more specific than one set for the channel.
func (i *instance) installLookups() error {
	vm := i.vm

	scopes := func() []*SharedMap {
		if i.ctx == nil {
			return nil
		}
		return []*SharedMap{
			i.ctx.ResponseMap,
			i.ctx.ConnectorMap,
			i.ctx.ChannelMap,
			i.ctx.SourceMap,
			i.eng.channelScope(i.ctx.ChannelName),
			i.eng.globalMap,
		}
	}

	if err := vm.Set("$", func(call goja.FunctionCall) goja.Value {
		key := call.Argument(0).String()
		// One argument reads; two arguments write to the channel map, which is
		// Mirth's shorthand for $c.
		if len(call.Arguments) > 1 {
			if i.ctx != nil && i.ctx.ChannelMap != nil {
				i.ctx.ChannelMap.Put(key, call.Argument(1).Export())
			}
			return goja.Undefined()
		}
		for _, scope := range scopes() {
			if scope == nil {
				continue
			}
			if v, ok := scope.Get(key); ok {
				return vm.ToValue(v)
			}
		}
		return goja.Undefined()
	}); err != nil {
		return err
	}

	// The scoped forms, so a script can be explicit.
	single := map[string]func() *SharedMap{
		"$r":  func() *SharedMap { return i.ctxMap(func(c *Context) *SharedMap { return c.ResponseMap }) },
		"$co": func() *SharedMap { return i.ctxMap(func(c *Context) *SharedMap { return c.ConnectorMap }) },
		"$c":  func() *SharedMap { return i.ctxMap(func(c *Context) *SharedMap { return c.ChannelMap }) },
		"$s":  func() *SharedMap { return i.ctxMap(func(c *Context) *SharedMap { return c.SourceMap }) },
		"$gc": func() *SharedMap {
			if i.ctx == nil {
				return nil
			}
			return i.eng.channelScope(i.ctx.ChannelName)
		},
		"$g": func() *SharedMap { return i.eng.globalMap },
	}
	for name, get := range single {
		getter := get
		if err := vm.Set(name, func(call goja.FunctionCall) goja.Value {
			scope := getter()
			if scope == nil {
				return goja.Undefined()
			}
			key := call.Argument(0).String()
			if len(call.Arguments) > 1 {
				scope.Put(key, call.Argument(1).Export())
				return goja.Undefined()
			}
			if v, ok := scope.Get(key); ok {
				return vm.ToValue(v)
			}
			return goja.Undefined()
		}); err != nil {
			return err
		}
	}
	return nil
}

func (i *instance) ctxMap(pick func(*Context) *SharedMap) *SharedMap {
	if i.ctx == nil {
		return nil
	}
	return pick(i.ctx)
}

func (i *instance) installUtilities() error {
	vm := i.vm

	// DateUtil, with Java's SimpleDateFormat patterns, because that is what the
	// scripts contain.
	dateUtil := vm.NewObject()
	if err := dateUtil.Set("getCurrentDate", func(call goja.FunctionCall) goja.Value {
		return vm.ToValue(formatJavaDate(time.Now(), call.Argument(0).String()))
	}); err != nil {
		return err
	}
	if err := dateUtil.Set("formatDate", func(call goja.FunctionCall) goja.Value {
		pattern := call.Argument(0).String()
		when := time.Now()
		if arg := call.Argument(1); !goja.IsUndefined(arg) {
			if exported, ok := arg.Export().(time.Time); ok {
				when = exported
			}
		}
		return vm.ToValue(formatJavaDate(when, pattern))
	}); err != nil {
		return err
	}
	if err := dateUtil.Set("getDate", func(call goja.FunctionCall) goja.Value {
		pattern, text := call.Argument(0).String(), call.Argument(1).String()
		when, err := parseJavaDate(text, pattern)
		if err != nil {
			panic(vm.NewGoError(err))
		}
		return vm.ToValue(when)
	}); err != nil {
		return err
	}
	if err := dateUtil.Set("convertDate", func(call goja.FunctionCall) goja.Value {
		in, out, text := call.Argument(0).String(), call.Argument(1).String(), call.Argument(2).String()
		when, err := parseJavaDate(text, in)
		if err != nil {
			// Mirth throws here. Returning the input unchanged would put an
			// unparsed date into a field typed as a timestamp, which the
			// receiver then rejects for a reason that points at the wrong place.
			panic(vm.NewGoError(fmt.Errorf("convertDate: %q does not match %q", text, in)))
		}
		return vm.ToValue(formatJavaDate(when, out))
	}); err != nil {
		return err
	}
	if err := vm.Set("DateUtil", dateUtil); err != nil {
		return err
	}
	if err := vm.Set("dateUtil", dateUtil); err != nil {
		return err
	}

	uuid := vm.NewObject()
	if err := uuid.Set("getUUID", func(goja.FunctionCall) goja.Value {
		return vm.ToValue(newUUID())
	}); err != nil {
		return err
	}
	if err := vm.Set("UUIDGenerator", uuid); err != nil {
		return err
	}

	// ChannelUtil, limited to what can be answered without the engine.
	channelUtil := vm.NewObject()
	if err := channelUtil.Set("getChannelName", func(goja.FunctionCall) goja.Value {
		if i.ctx == nil {
			return goja.Undefined()
		}
		return vm.ToValue(i.ctx.ChannelName)
	}); err != nil {
		return err
	}
	if err := channelUtil.Set("getChannelId", func(call goja.FunctionCall) goja.Value {
		// Perfuse identifies channels by name, not by a generated id. Returning
		// the name is more useful than returning a fabricated identifier that
		// matches nothing.
		return vm.ToValue(call.Argument(0).String())
	}); err != nil {
		return err
	}
	return vm.Set("ChannelUtil", channelUtil)
}

func (i *instance) installSerializers() error {
	vm := i.vm

	makeSerializer := func(kind string) *goja.Object {
		s := vm.NewObject()
		_ = s.Set("toXML", func(call goja.FunctionCall) goja.Value {
			input := call.Argument(0).String()
			switch strings.ToUpper(kind) {
			case "HL7V2", "HL7":
				root, err := hl7xml.FromRaw([]byte(normaliseER7(input)))
				if err != nil {
					panic(vm.NewGoError(fmt.Errorf("toXML: %w", err)))
				}
				return vm.ToValue(string(root.Marshal(0)))
			case "JSON":
				var any any
				if err := json.Unmarshal([]byte(input), &any); err != nil {
					panic(vm.NewGoError(fmt.Errorf("toXML: %w", err)))
				}
				return vm.ToValue(jsonToXML("json", any).String())
			default:
				return vm.ToValue(input)
			}
		})
		_ = s.Set("fromXML", func(call goja.FunctionCall) goja.Value {
			input := call.Argument(0)
			var root *xtree.Node
			if x, ok := xmlNodes(input); ok && len(x) > 0 {
				root = x[0]
			} else {
				parsed, err := xtree.Parse([]byte(input.String()))
				if err != nil {
					panic(vm.NewGoError(fmt.Errorf("fromXML: %w", err)))
				}
				root = parsed
			}
			switch strings.ToUpper(kind) {
			case "HL7V2", "HL7":
				out, err := hl7xml.ToER7(root, hl7xml.DefaultOptions())
				if err != nil {
					panic(vm.NewGoError(fmt.Errorf("fromXML: %w", err)))
				}
				return vm.ToValue(string(out))
			default:
				return vm.ToValue(root.String())
			}
		})
		return s
	}

	factory := vm.NewObject()
	if err := factory.Set("getSerializer", func(call goja.FunctionCall) goja.Value {
		return makeSerializer(call.Argument(0).String())
	}); err != nil {
		return err
	}
	if err := factory.Set("getHL7Serializer", func(goja.FunctionCall) goja.Value {
		return makeSerializer("HL7V2")
	}); err != nil {
		return err
	}
	return vm.Set("SerializerFactory", factory)
}

// installFileUtil adds Mirth's FileUtil, gated on permission.
func (i *instance) installFileUtil() error {
	vm := i.vm
	fileUtil := vm.NewObject()

	deny := func(method string) goja.Value {
		panic(vm.NewGoError(fmt.Errorf(
			"FileUtil.%s is not permitted: add 'file' to this channel's script permissions to allow it", method)))
	}

	if err := fileUtil.Set("read", func(call goja.FunctionCall) goja.Value {
		if !i.eng.allowed(PermFile) {
			return deny("read")
		}
		path, err := i.eng.opts.FileRoots.Check(call.Argument(0).String())
		if err != nil {
			panic(vm.NewGoError(fmt.Errorf("FileUtil.read: %w", err)))
		}
		data, err := os.ReadFile(path)
		if err != nil {
			panic(vm.NewGoError(err))
		}
		return vm.ToValue(string(data))
	}); err != nil {
		return err
	}

	if err := fileUtil.Set("write", func(call goja.FunctionCall) goja.Value {
		if !i.eng.allowed(PermFile) {
			return deny("write")
		}
		path, err := i.eng.opts.FileRoots.Check(call.Argument(0).String())
		if err != nil {
			panic(vm.NewGoError(fmt.Errorf("FileUtil.write: %w", err)))
		}
		appendMode := call.Argument(1).ToBoolean()
		data := call.Argument(2).String()

		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			panic(vm.NewGoError(err))
		}
		flags := os.O_CREATE | os.O_WRONLY
		if appendMode {
			flags |= os.O_APPEND
		} else {
			flags |= os.O_TRUNC
		}
		f, err := os.OpenFile(path, flags, 0o640)
		if err != nil {
			panic(vm.NewGoError(err))
		}
		defer f.Close()
		if _, err := f.WriteString(data); err != nil {
			panic(vm.NewGoError(err))
		}
		return vm.ToValue(true)
	}); err != nil {
		return err
	}

	// Base64 needs no permission: it touches nothing outside the process, and
	// it is how a CDA arrives inside an HL7 message.
	if err := fileUtil.Set("encode", func(call goja.FunctionCall) goja.Value {
		return vm.ToValue(base64.StdEncoding.EncodeToString([]byte(call.Argument(0).String())))
	}); err != nil {
		return err
	}
	if err := fileUtil.Set("decode", func(call goja.FunctionCall) goja.Value {
		data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(call.Argument(0).String()))
		if err != nil {
			panic(vm.NewGoError(fmt.Errorf("FileUtil.decode: %w", err)))
		}
		return vm.ToValue(string(data))
	}); err != nil {
		return err
	}

	if err := vm.Set("FileUtil", fileUtil); err != nil {
		return err
	}

	// DatabaseConnectionFactory is refused with an explanation rather than
	// stubbed, because a transformer that silently reads nothing from a database
	// produces messages that are wrong in a way no test catches.
	dbFactory := vm.NewObject()
	refuse := func(goja.FunctionCall) goja.Value {
		panic(vm.NewGoError(fmt.Errorf(
			"DatabaseConnectionFactory is not implemented in Perfuse. " +
				"A database lookup inside a transformer is also the most common cause of a " +
				"Mirth channel stalling under load; consider a database destination or a " +
				"cached lookup table instead")))
	}
	if err := dbFactory.Set("createDatabaseConnection", refuse); err != nil {
		return err
	}
	return vm.Set("DatabaseConnectionFactory", dbFactory)
}

func (i *instance) installRouter() error {
	vm := i.vm
	router := vm.NewObject()

	send := func(call goja.FunctionCall) goja.Value {
		if !i.eng.allowed(PermRoute) {
			panic(vm.NewGoError(fmt.Errorf(
				"router.routeMessage is not permitted: add 'route' to this channel's script permissions")))
		}
		fn, ok := i.eng.route.get()
		if !ok || fn == nil {
			panic(vm.NewGoError(fmt.Errorf(
				"router.routeMessage is unavailable: this script is running outside a live engine")))
		}
		channel := call.Argument(0).String()
		payload := call.Argument(1)

		var raw string
		if nodes, ok := xmlNodes(payload); ok && len(nodes) > 0 {
			raw = er7(nodes[0])
		} else {
			raw = normaliseER7(payload.String())
		}
		if err := fn(channel, []byte(raw)); err != nil {
			panic(vm.NewGoError(fmt.Errorf("routeMessage to %q: %w", channel, err)))
		}
		return goja.Undefined()
	}

	if err := router.Set("routeMessage", send); err != nil {
		return err
	}
	if err := router.Set("routeMessageByChannelId", send); err != nil {
		return err
	}
	return vm.Set("router", router)
}

// installJavaRefusal makes Java interoperability fail with a message that names
// what was reached for.
//
// Mirth scripts can call arbitrary Java. Those calls cannot work here, and the
// only useful behaviour is to say so at the exact line, naming the class, so
// somebody can decide what to do. Returning undefined would surface as a
// confusing failure several lines later.
func (i *instance) installJavaRefusal() error {
	vm := i.vm

	var makeProxy func(path string) *goja.Object
	makeProxy = func(path string) *goja.Object {
		obj := vm.NewObject()
		proxy := vm.NewProxy(obj, &goja.ProxyTrapConfig{
			Get: func(target *goja.Object, key string, receiver goja.Value) goja.Value {
				full := path + "." + key
				// A capitalised final segment is a class name; anything else is
				// still a package, so keep descending.
				if key != "" && key[0] >= 'A' && key[0] <= 'Z' {
					panic(vm.NewGoError(fmt.Errorf(
						"this script uses Java: %s. Perfuse runs JavaScript but has no Java runtime, "+
							"so this call cannot work. Rewrite it in JavaScript, or move the work into a "+
							"channel step", full)))
				}
				return makeProxy(full)
			},
		})
		return vm.ToValue(proxy).ToObject(vm)
	}

	packages := vm.NewObject()
	for _, root := range []string{"java", "javax", "org", "com", "net"} {
		if err := packages.Set(root, makeProxy("Packages."+root)); err != nil {
			return err
		}
	}
	if err := vm.Set("Packages", packages); err != nil {
		return err
	}
	if err := vm.Set("java", makeProxy("java")); err != nil {
		return err
	}
	if err := vm.Set("javax", makeProxy("javax")); err != nil {
		return err
	}

	// importPackage is Rhino's, and a script that calls it is about to use Java.
	for _, name := range []string{"importPackage", "importClass"} {
		fn := name
		if err := vm.Set(name, func(call goja.FunctionCall) goja.Value {
			panic(vm.NewGoError(fmt.Errorf(
				"%s is a Rhino feature for loading Java classes and has no equivalent here", fn)))
		}); err != nil {
			return err
		}
	}
	return nil
}

// wrapMap presents a SharedMap with Java's Map methods, since that is what the
// scripts call, plus index access for convenience.
func (i *instance) wrapMap(m *SharedMap) goja.Value {
	return i.vm.NewDynamicObject(&mapObject{m: m, vm: i.vm})
}

type mapObject struct {
	m  *SharedMap
	vm *goja.Runtime
}

func (o *mapObject) Get(key string) goja.Value {
	switch key {
	case "put":
		return o.vm.ToValue(func(call goja.FunctionCall) goja.Value {
			o.m.Put(call.Argument(0).String(), call.Argument(1).Export())
			return goja.Undefined()
		})
	case "get":
		return o.vm.ToValue(func(call goja.FunctionCall) goja.Value {
			if v, ok := o.m.Get(call.Argument(0).String()); ok {
				return o.vm.ToValue(v)
			}
			return goja.Undefined()
		})
	case "containsKey", "has":
		return o.vm.ToValue(func(call goja.FunctionCall) goja.Value {
			_, ok := o.m.Get(call.Argument(0).String())
			return o.vm.ToValue(ok)
		})
	case "remove", "delete":
		return o.vm.ToValue(func(call goja.FunctionCall) goja.Value {
			return o.vm.ToValue(o.m.Remove(call.Argument(0).String()))
		})
	case "size":
		return o.vm.ToValue(func(goja.FunctionCall) goja.Value { return o.vm.ToValue(o.m.Len()) })
	case "isEmpty":
		return o.vm.ToValue(func(goja.FunctionCall) goja.Value { return o.vm.ToValue(o.m.Len() == 0) })
	case "clear":
		return o.vm.ToValue(func(goja.FunctionCall) goja.Value {
			o.m.Clear()
			return goja.Undefined()
		})
	case "keySet", "keys":
		return o.vm.ToValue(func(goja.FunctionCall) goja.Value { return o.vm.ToValue(o.m.Keys()) })
	case "toString":
		return o.vm.ToValue(func(goja.FunctionCall) goja.Value { return o.vm.ToValue(o.m.String()) })
	}
	if v, ok := o.m.Get(key); ok {
		return o.vm.ToValue(v)
	}
	return goja.Undefined()
}

func (o *mapObject) Set(key string, val goja.Value) bool {
	o.m.Put(key, val.Export())
	return true
}

func (o *mapObject) Has(key string) bool {
	_, ok := o.m.Get(key)
	return ok
}

func (o *mapObject) Delete(key string) bool { return o.m.Remove(key) }
func (o *mapObject) Keys() []string         { return o.m.Keys() }

// SharedMap is a concurrency-safe string-keyed map.
//
// globalMap and globalChannelMap outlive a single message and are reachable from
// every channel at once, so they cannot be plain maps: two messages arriving
// together on different connectors would race. Mirth uses synchronised maps for
// the same reason.
type SharedMap struct {
	mu     sync.RWMutex
	values map[string]any
	order  []string
}

// NewSharedMap makes an empty map.
func NewSharedMap() *SharedMap { return &SharedMap{values: map[string]any{}} }

// Put stores a value.
func (m *SharedMap) Put(key string, value any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.values == nil {
		m.values = map[string]any{}
	}
	if _, exists := m.values[key]; !exists {
		// Insertion order is kept so that a script iterating keys sees a stable
		// sequence, which makes a transformer's output reproducible.
		m.order = append(m.order, key)
	}
	m.values[key] = value
}

// Get reads a value.
func (m *SharedMap) Get(key string) (any, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.values[key]
	return v, ok
}

// Remove deletes a key, reporting whether it was there.
func (m *SharedMap) Remove(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.values[key]; !ok {
		return false
	}
	delete(m.values, key)
	for i, k := range m.order {
		if k == key {
			m.order = append(m.order[:i], m.order[i+1:]...)
			break
		}
	}
	return true
}

// Len reports how many entries there are.
func (m *SharedMap) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.values)
}

// Clear empties the map.
func (m *SharedMap) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.values = map[string]any{}
	m.order = nil
}

// Keys returns the keys in insertion order.
func (m *SharedMap) Keys() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, len(m.order))
	copy(out, m.order)
	return out
}

// Snapshot copies the contents, for showing a map in the interface without
// holding the lock while rendering.
func (m *SharedMap) Snapshot() map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]any, len(m.values))
	for k, v := range m.values {
		out[k] = v
	}
	return out
}

func (m *SharedMap) String() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range m.order {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s=%v", k, m.values[k])
	}
	b.WriteByte('}')
	return b.String()
}

// xmlNodes recovers tree nodes from a JavaScript value that might be an E4X
// object.
func xmlNodes(v goja.Value) ([]*xtree.Node, bool) {
	obj, ok := v.(*goja.Object)
	if !ok {
		return nil, false
	}
	dyn, ok := obj.Export().(goja.DynamicObject)
	if !ok {
		return nil, false
	}
	x, ok := dyn.(interface{ Nodes() []*xtree.Node })
	if !ok {
		return nil, false
	}
	return x.Nodes(), true
}

// normaliseER7 accepts a message whose segments are separated by line feeds,
// which is how one looks after a trip through a text editor or a JSON payload.
func normaliseER7(s string) string {
	if strings.Contains(s, "\r") {
		return s
	}
	s = strings.ReplaceAll(s, "\n", "\r")
	if !strings.HasSuffix(s, "\r") {
		s += "\r"
	}
	return s
}

// jsonToXML gives SerializerFactory's JSON serializer something to return.
func jsonToXML(name string, value any) *xtree.Node {
	node := xtree.New(name)
	switch v := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		// Sorted, so the same input always produces the same output.
		for i := 0; i < len(keys); i++ {
			for j := i + 1; j < len(keys); j++ {
				if keys[j] < keys[i] {
					keys[i], keys[j] = keys[j], keys[i]
				}
			}
		}
		for _, k := range keys {
			node.Append(jsonToXML(k, v[k]))
		}
	case []any:
		for _, item := range v {
			node.Append(jsonToXML("item", item))
		}
	case nil:
		node.SetText("")
	case float64:
		node.SetText(strconv.FormatFloat(v, 'f', -1, 64))
	case bool:
		node.SetText(strconv.FormatBool(v))
	default:
		node.SetText(fmt.Sprint(v))
	}
	return node
}

func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
