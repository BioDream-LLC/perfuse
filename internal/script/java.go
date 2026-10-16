package script

import (
	_ "embed"
	"encoding/base64"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/dop251/goja"
)

// javaJS is the JavaScript equivalents of common Java classes (java.util collections, StringBuilder, Integer, SimpleDateFormat,
// Pattern, Base64, StringUtils ...), so a migrated Mirth script that calls them runs unchanged.
//
//go:embed java.js
var javaJS string

var (
	javaOnce    sync.Once
	javaProgram *goja.Program
	javaErr     error
	javaNames   []string
)

func javaPrelude() (*goja.Program, error) {
	javaOnce.Do(func() {
		javaProgram, javaErr = goja.Compile("java.js", javaJS, true)
	})
	return javaProgram, javaErr
}

// JavaClasses lists every Java class name Perfuse provides a JavaScript equivalent for, sorted. The Mirth importer uses it so
// it reports these calls as working rather than as needing a rewrite.
func JavaClasses() []string {
	javaNamesOnce.Do(func() {
		vm := goja.New()
		classes, err := loadJavaClasses(vm)
		if err != nil {
			return
		}
		javaNames = classes.Keys()
		sort.Strings(javaNames)
	})
	return javaNames
}

var javaNamesOnce sync.Once

// loadJavaClasses runs the prelude in vm and returns its table of classes by full name.
func loadJavaClasses(vm *goja.Runtime) (*goja.Object, error) {
	prog, err := javaPrelude()
	if err != nil {
		return nil, fmt.Errorf("the Java equivalents did not compile: %w", err)
	}
	fnVal, err := vm.RunProgram(prog)
	if err != nil {
		return nil, fmt.Errorf("the Java equivalents did not load: %w", err)
	}
	fn, ok := goja.AssertFunction(fnVal)
	if !ok {
		return nil, fmt.Errorf("the Java equivalents are not a function")
	}
	h := javaHelpers(vm)
	out, err := fn(goja.Undefined(), h)
	if err != nil {
		return nil, fmt.Errorf("the Java equivalents failed to start: %w", err)
	}
	return out.ToObject(vm), nil
}

// javaHelpers are what the JavaScript side cannot do itself: UTF-8 and base64 over byte arrays, Java date patterns, UUIDs.
func javaHelpers(vm *goja.Runtime) *goja.Object {
	h := vm.NewObject()
	toBytes := func(v goja.Value) []byte {
		obj, ok := v.(*goja.Object)
		if !ok || obj == nil {
			return []byte(v.String())
		}
		n := int(obj.Get("length").ToInteger())
		b := make([]byte, n)
		for i := 0; i < n; i++ {
			b[i] = byte(obj.Get(fmt.Sprint(i)).ToInteger())
		}
		return b
	}
	fromBytes := func(b []byte) goja.Value {
		a := make([]any, len(b))
		for i, c := range b {
			a[i] = int64(c)
		}
		return vm.NewArray(a...)
	}
	_ = h.Set("utf8Encode", func(call goja.FunctionCall) goja.Value { return fromBytes([]byte(call.Argument(0).String())) })
	_ = h.Set("utf8Decode", func(call goja.FunctionCall) goja.Value {
		b := toBytes(call.Argument(0))
		if !utf8.Valid(b) {
			return vm.ToValue(strings.ToValidUTF8(string(b), "\uFFFD"))
		}
		return vm.ToValue(string(b))
	})
	_ = h.Set("b64Encode", func(call goja.FunctionCall) goja.Value {
		return vm.ToValue(base64.StdEncoding.EncodeToString(toBytes(call.Argument(0))))
	})
	_ = h.Set("b64Decode", func(call goja.FunctionCall) goja.Value {
		s := strings.Map(func(r rune) rune {
			if r == '\r' || r == '\n' || r == ' ' || r == '\t' {
				return -1
			}
			return r
		}, call.Argument(0).String())
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			if b, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(s, "=")); err != nil {
				panic(vm.NewGoError(fmt.Errorf("decodeBase64: not base64: %w", err)))
			}
		}
		return fromBytes(b)
	})
	_ = h.Set("dateFormat", func(call goja.FunctionCall) goja.Value {
		when := time.Now()
		if t, ok := call.Argument(1).Export().(time.Time); ok {
			when = t
		}
		return vm.ToValue(formatJavaDate(when, call.Argument(0).String()))
	})
	_ = h.Set("dateParse", func(call goja.FunctionCall) goja.Value {
		when, err := parseJavaDate(call.Argument(1).String(), call.Argument(0).String())
		if err != nil {
			panic(vm.NewGoError(fmt.Errorf("java.text.ParseException: %w", err)))
		}
		return jsDate(vm, when)
	})
	_ = h.Set("uuid", func(goja.FunctionCall) goja.Value { return vm.ToValue(newUUID()) })
	return h
}

// jsDate makes a JavaScript Date, which is what a script expects back where Java would give a java.util.Date: getTime,
// getFullYear and the rest work on it, and DateUtil.formatDate takes it.
func jsDate(vm *goja.Runtime, t time.Time) goja.Value {
	d, err := vm.New(vm.Get("Date"), vm.ToValue(t.UnixMilli()))
	if err != nil {
		return vm.ToValue(t)
	}
	return d
}
