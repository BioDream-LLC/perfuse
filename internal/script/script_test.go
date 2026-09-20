package script

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/hl7xml"
)

const adt = "MSH|^~\\&|SENDAPP|SITEA|RECV|RFAC|20260818120000||ADT^A01^ADT_A01|CTRL1|P|2.5.1\r" +
	"EVN|A01|20260818115900\r" +
	"PID|1||MRN9^^^SITEA^MR~999^^^SSA^SS||Doe^Jane^Q^^Ms.||19800101|F|||4 Elm Rd^^Vestavia^AL^35216\r" +
	"PV1|1|I|ICU^7^01^SITEA||||1234^Smith^Sam|||MED\r" +
	"OBX|1|NM|718-7^Hemoglobin^LN||13.5|g/dL|12.0-16.0|N|||F\r" +
	"OBX|2|NM|6690-2^Leukocytes^LN||14.2|10*3/uL|4.0-11.0|H|||F\r"

func newTestEngine(t *testing.T, perms ...Permission) *Engine {
	t.Helper()
	return New(Options{Timeout: 3 * time.Second, Permissions: perms})
}

// runScript compiles and runs a script against the sample message, returning the
// filter verdict and the resulting HL7.
func runScript(t *testing.T, e *Engine, kind Kind, src string) (Result, string) {
	t.Helper()

	root, err := hl7xml.FromRaw([]byte(adt))
	if err != nil {
		t.Fatal(err)
	}

	s, err := e.Compile("test", src, kind)
	if err != nil {
		t.Fatalf("compile failed: %v", err)
	}

	ctx := &Context{
		Message:      root,
		ChannelName:  "adt-inbound",
		ChannelMap:   NewSharedMap(),
		ConnectorMap: NewSharedMap(),
		ResponseMap:  NewSharedMap(),
		SourceMap:    NewSharedMap(),
	}

	res, err := e.Run(s, ctx)
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}

	out, err := hl7xml.ToER7(root, hl7xml.DefaultOptions())
	if err != nil {
		return res, ""
	}
	return res, string(out)
}

// TestMirthFilters uses filters written the way Mirth writes them, with an
// explicit return.
func TestMirthFilters(t *testing.T) {
	e := newTestEngine(t)

	for _, tc := range []struct {
		name   string
		src    string
		accept bool
	}{
		{"accept on message type", `return msg['MSH']['MSH.9']['MSH.9.2'].toString() == 'A01';`, true},
		{"reject on message type", `return msg['MSH']['MSH.9']['MSH.9.2'].toString() == 'A28';`, false},
		{"patient class", `return msg['PV1']['PV1.2']['PV1.2.1'].toString() == 'I';`, true},
		{
			"reject when a required field is missing",
			`var mrn = msg['PID']['PID.3']['PID.3.1'].toString();
			 if (mrn == '') { return false; }
			 return true;`,
			true,
		},
		{
			"loop to find an abnormal result",
			`for each (var obx in msg['OBX']) {
			   if (obx['OBX.8']['OBX.8.1'].toString() == 'H') { return true; }
			 }
			 return false;`,
			true,
		},
		{
			"regular expression on an identifier",
			`return /^MRN\d+$/.test(msg['PID']['PID.3']['PID.3.1'].toString());`,
			true,
		},
		{
			"a filter with no return accepts",
			`var x = 1;`,
			true,
		},
		{
			"explicit false",
			`return false;`,
			false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, _ := runScript(t, e, Filter, tc.src)
			if res.Accept != tc.accept {
				t.Errorf("accept = %v, want %v", res.Accept, tc.accept)
			}
		})
	}
}

// TestMirthTransformers uses transformers in the style found in real channels.
func TestMirthTransformers(t *testing.T) {
	e := newTestEngine(t)

	for _, tc := range []struct {
		name   string
		src    string
		expect string
		absent string
	}{
		{
			// Indexed with [0], because PID-3 here has two repetitions and an
			// unindexed read would concatenate both. See TestListSemanticsMatchE4X.
			name: "pad an MRN to ten characters",
			src: `var mrn = msg['PID']['PID.3'][0]['PID.3.1'].toString();
			         while (mrn.length < 10) { mrn = '0' + mrn; }
			         msg['PID']['PID.3'][0]['PID.3.1'] = mrn;`,
			expect: "000000MRN9",
		},
		{
			name: "map a code through a lookup",
			src: `var map = {'MED':'Medicine','SUR':'Surgery'};
			         var svc = msg['PV1']['PV1.10']['PV1.10.1'].toString();
			         if (map[svc]) { msg['PV1']['PV1.10']['PV1.10.1'] = map[svc]; }`,
			expect: "Medicine",
		},
		{
			name: "reformat a date with DateUtil",
			src: `var dob = msg['PID']['PID.7']['PID.7.1'].toString();
			         msg['PID']['PID.7']['PID.7.1'] = DateUtil.convertDate('yyyyMMdd', 'MM/dd/yyyy', dob);`,
			expect: "01/01/1980",
		},
		{
			name:   "stamp a generated identifier",
			src:    `msg['MSH']['MSH.10']['MSH.10.1'] = UUIDGenerator.getUUID();`,
			absent: "CTRL1",
		},
		{
			name:   "use validate for a default",
			src:    `msg['PID']['PID.6']['PID.6.1'] = validate(msg['PID']['PID.6']['PID.6.1'], 'UNKNOWN', null);`,
			expect: "UNKNOWN",
		},
		{
			name: "store a value in the channel map and read it back",
			src: `channelMap.put('mrn', msg['PID']['PID.3'][0]['PID.3.1'].toString());
			         msg['PID']['PID.4']['PID.4.1'] = channelMap.get('mrn');`,
			expect: "~999^^^SSA^SS|MRN9|",
		},
		{
			name: "the dollar shorthand reads the channel map",
			src: `channelMap.put('site', 'SITEA');
			         msg['PID']['PID.4']['PID.4.1'] = $('site');`,
			expect: "|SITEA|",
		},
		{
			name: "concatenate a full name",
			src: `var n = msg['PID']['PID.5'];
			         msg['PID']['PID.9']['PID.9.1'] = n['PID.5.2'].toString() + ' ' + n['PID.5.1'].toString();`,
			expect: "Jane Doe",
		},
		{
			name:   "delete a segment conditionally",
			src:    `if (msg['MSH']['MSH.9']['MSH.9.2'].toString() == 'A01') { delete msg['EVN']; }`,
			absent: "EVN|",
		},
		{
			name:   "append a custom segment",
			src:    `msg.appendChild(<ZPI><ZPI.1><ZPI.1.1>PERFUSE</ZPI.1.1></ZPI.1></ZPI>);`,
			expect: "ZPI|PERFUSE",
		},
		{
			name: "loop with an index over results",
			src: `for (var i = 0; i < msg['OBX'].length(); i++) {
			           msg['OBX'][i]['OBX.11']['OBX.11.1'] = 'C';
			         }`,
			expect: "12.0-16.0|N|||C",
		},
		{
			name: "trim whitespace from every component of an address",
			src: `var addr = msg['PID']['PID.11'];
			         for each (var part in addr.children()) {
			           part['' + part.name() + '.1'] = part.toString().trim();
			         }`,
			expect: "Vestavia",
		},
		{
			name: "serializer converts the message to XML and back",
			src: `var ser = SerializerFactory.getSerializer('HL7V2');
			         var er7 = ser.fromXML(msg.toXMLString());
			         if (er7.indexOf('MSH|') !== 0) { throw 'expected a message, got ' + er7.substring(0, 20); }
			         var backToXml = ser.toXML(er7);
			         if (backToXml.indexOf('<HL7Message>') !== 0) { throw 'expected xml'; }
			         msg['PID']['PID.4']['PID.4.1'] = 'roundtripped';`,
			expect: "|roundtripped|",
		},
		{
			name: "base64 decode a payload",
			src: `var decoded = FileUtil.decode('SGVsbG8=');
			         msg['PID']['PID.4']['PID.4.1'] = decoded;`,
			expect: "|Hello|",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, out := runScript(t, e, Transformer, tc.src)
			if tc.expect != "" && !strings.Contains(out, tc.expect) {
				t.Errorf("output missing %q:\n%s", tc.expect, out)
			}
			if tc.absent != "" && strings.Contains(out, tc.absent) {
				t.Errorf("output should not contain %q:\n%s", tc.absent, out)
			}
		})
	}
}

// TestListSemanticsMatchE4X pins behaviour that surprises people and is worth
// being explicit about, because it is the most likely source of a "my script
// behaves differently" report.
//
// In E4X a field with two repetitions is a list, so reading it concatenates both
// and assigning to it writes both. Rhino does exactly this, so Mirth does too,
// and a script written against a single-repetition feed changes meaning the day a
// second identifier appears. Reproducing the behaviour is correct; hiding it
// would mean a ported channel produced different output from the original.
func TestListSemanticsMatchE4X(t *testing.T) {
	e := newTestEngine(t)

	res, _ := runScript(t, e, Filter,
		`return msg['PID']['PID.3']['PID.3.1'].toString();`)
	if !res.Accept {
		t.Error("a non-empty concatenation should be truthy")
	}

	// Reading without an index concatenates every repetition.
	root, _ := hl7xml.FromRaw([]byte(adt))
	s, err := e.Compile("concat",
		`channelMap.put('all', msg['PID']['PID.3']['PID.3.1'].toString());
		 channelMap.put('first', msg['PID']['PID.3'][0]['PID.3.1'].toString());
		 channelMap.put('count', msg['PID']['PID.3'].length());`, Transformer)
	if err != nil {
		t.Fatal(err)
	}
	m := NewSharedMap()
	if _, err := e.Run(s, &Context{Message: root, ChannelMap: m}); err != nil {
		t.Fatal(err)
	}

	if v, _ := m.Get("all"); v != "MRN9999" {
		t.Errorf("unindexed read = %v, want the concatenation MRN9999", v)
	}
	if v, _ := m.Get("first"); v != "MRN9" {
		t.Errorf("indexed read = %v, want MRN9", v)
	}
	if v, _ := m.Get("count"); fmt.Sprint(v) != "2" {
		t.Errorf("length() = %v, want 2", v)
	}
}

// TestLoggerIsCaptured checks the logger reaches the caller, since that is how an
// analyst debugs a transformer.
func TestLoggerIsCaptured(t *testing.T) {
	e := newTestEngine(t)
	res, _ := runScript(t, e, Transformer,
		`logger.info('mrn is', msg['PID']['PID.3']['PID.3.1'].toString());
		 logger.error('something to look at');`)

	if len(res.Logs) != 2 {
		t.Fatalf("got %d log lines, want 2: %+v", len(res.Logs), res.Logs)
	}
	if res.Logs[0].Level != "info" || !strings.Contains(res.Logs[0].Message, "MRN9") {
		t.Errorf("first line = %+v", res.Logs[0])
	}
	if res.Logs[1].Level != "error" {
		t.Errorf("second line = %+v", res.Logs[1])
	}
}

// TestTimeoutStopsRunawayScript is the safety property that Mirth lacks. A
// channel must not be taken down by a loop in a transformer.
func TestTimeoutStopsRunawayScript(t *testing.T) {
	e := New(Options{Timeout: 250 * time.Millisecond})

	root, err := hl7xml.FromRaw([]byte(adt))
	if err != nil {
		t.Fatal(err)
	}
	s, err := e.Compile("runaway", `while (true) { }`, Transformer)
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	_, err = e.Run(s, &Context{Message: root, ChannelMap: NewSharedMap()})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("an infinite loop should be stopped")
	}
	if !strings.Contains(err.Error(), "still running") {
		t.Errorf("error should explain the timeout, got: %v", err)
	}
	if elapsed > 3*time.Second {
		t.Errorf("timeout took %s, far longer than configured", elapsed)
	}
}

// TestRuntimeIsReusableAfterTimeout checks a pooled runtime is not poisoned by an
// interrupt, because otherwise one bad message disables the channel.
func TestRuntimeIsReusableAfterTimeout(t *testing.T) {
	e := New(Options{Timeout: 200 * time.Millisecond, MaxVMs: 1})

	runaway, err := e.Compile("runaway", `while (true) { }`, Transformer)
	if err != nil {
		t.Fatal(err)
	}
	root, _ := hl7xml.FromRaw([]byte(adt))
	if _, err := e.Run(runaway, &Context{Message: root, ChannelMap: NewSharedMap()}); err == nil {
		t.Fatal("expected a timeout")
	}

	// The same engine must still work.
	res, out := runScript(t, e, Transformer, `msg['PID']['PID.4']['PID.4.1'] = 'recovered';`)
	if !res.Accept {
		t.Error("a transformer should report accept")
	}
	if !strings.Contains(out, "recovered") {
		t.Errorf("the engine did not recover after a timeout:\n%s", out)
	}
}

// TestNoDataLeaksBetweenMessages is a privacy property, not a tidiness one: a
// pooled runtime that kept the previous msg binding would expose one patient's
// data to the next message's script.
func TestNoDataLeaksBetweenMessages(t *testing.T) {
	e := New(Options{Timeout: time.Second, MaxVMs: 1})

	first, _ := hl7xml.FromRaw([]byte(adt))
	s, err := e.Compile("read", `channelMap.put('seen', msg['PID']['PID.3'][0]['PID.3.1'].toString());`, Transformer)
	if err != nil {
		t.Fatal(err)
	}
	m1 := NewSharedMap()
	if _, err := e.Run(s, &Context{Message: first, ChannelMap: m1}); err != nil {
		t.Fatal(err)
	}
	if v, _ := m1.Get("seen"); v != "MRN9" {
		t.Fatalf("first message read %v", v)
	}

	// A second message with a different patient, run on the same pooled VM.
	other := strings.Replace(adt, "MRN9", "MRN2222", 1)
	second, err := hl7xml.FromRaw([]byte(other))
	if err != nil {
		t.Fatal(err)
	}
	m2 := NewSharedMap()
	if _, err := e.Run(s, &Context{Message: second, ChannelMap: m2}); err != nil {
		t.Fatal(err)
	}
	if v, _ := m2.Get("seen"); v != "MRN2222" {
		t.Errorf("second message read %v, want MRN2222; a stale binding leaked", v)
	}
}

// TestGlobalMapPersistsAcrossMessages is the point of globalMap.
func TestGlobalMapPersistsAcrossMessages(t *testing.T) {
	e := newTestEngine(t)
	s, err := e.Compile("count",
		`var n = globalMap.get('count'); if (!n) n = 0; globalMap.put('count', n + 1);`, Transformer)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		root, _ := hl7xml.FromRaw([]byte(adt))
		if _, err := e.Run(s, &Context{Message: root, ChannelMap: NewSharedMap()}); err != nil {
			t.Fatal(err)
		}
	}
	if v, _ := e.globalMap.Get("count"); v != int64(3) && v != 3.0 && v != 3 {
		t.Errorf("globalMap count = %v (%T), want 3", v, v)
	}
}

// TestJavaIsRefusedClearly checks the failure mode for the scripts that cannot be
// ported. The error has to name the class, because that is the information
// somebody needs to decide what to do.
func TestJavaIsRefusedClearly(t *testing.T) {
	e := newTestEngine(t)

	for _, tc := range []struct{ name, src, mentions string }{
		{"java.util", `var d = new java.util.Date();`, "java.util.Date"},
		{"Packages", `var s = new Packages.java.lang.String('x');`, "java.lang.String"},
		{"importPackage", `importPackage(java.sql);`, "Rhino"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, _ := hl7xml.FromRaw([]byte(adt))
			s, err := e.Compile("java", tc.src, Transformer)
			if err != nil {
				t.Fatalf("should compile, and fail at run time: %v", err)
			}
			_, err = e.Run(s, &Context{Message: root, ChannelMap: NewSharedMap()})
			if err == nil {
				t.Fatal("Java use should fail")
			}
			if !strings.Contains(err.Error(), tc.mentions) {
				t.Errorf("error should mention %q, got: %v", tc.mentions, err)
			}
		})
	}
}

// TestFilePermissionIsEnforced checks the default denies filesystem access and
// says how to grant it.
func TestFilePermissionIsEnforced(t *testing.T) {
	denied := newTestEngine(t)
	root, _ := hl7xml.FromRaw([]byte(adt))
	s, err := denied.Compile("f", `FileUtil.write('/tmp/perfuse-should-not-exist', false, 'x');`, Transformer)
	if err != nil {
		t.Fatal(err)
	}
	_, err = denied.Run(s, &Context{Message: root, ChannelMap: NewSharedMap()})
	if err == nil {
		t.Fatal("file access should be denied by default")
	}
	if !strings.Contains(err.Error(), "script permissions") {
		t.Errorf("the error should say how to allow it, got: %v", err)
	}

	// Granted, and confined to a temporary directory. Unconfined file access is refused outright now, so a test
	// engine with PermFile and no roots would fail - which is the point.
	dir := t.TempDir()
	roots, err := NewFileRoots([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	granted := New(Options{Timeout: 3 * time.Second, Permissions: []Permission{PermFile}, FileRoots: roots})
	path := dir + "/out.txt"
	root2, _ := hl7xml.FromRaw([]byte(adt))
	s2, err := granted.Compile("f", `FileUtil.write('`+path+`', false, 'written');`, Transformer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := granted.Run(s2, &Context{Message: root2, ChannelMap: NewSharedMap()}); err != nil {
		t.Fatalf("with permission it should work: %v", err)
	}
}

func TestDatabaseIsRefusedWithAdvice(t *testing.T) {
	e := newTestEngine(t, PermDatabase)
	root, _ := hl7xml.FromRaw([]byte(adt))
	s, err := e.Compile("db", `DatabaseConnectionFactory.createDatabaseConnection('x','y','z','w');`, Transformer)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.Run(s, &Context{Message: root, ChannelMap: NewSharedMap()})
	if err == nil {
		t.Fatal("database access is not implemented and should say so")
	}
	if !strings.Contains(err.Error(), "not implemented") {
		t.Errorf("error should be explicit, got: %v", err)
	}
}

// TestJavaDateFormats covers the patterns that appear in real channels.
func TestJavaDateFormats(t *testing.T) {
	when := time.Date(2026, 8, 18, 14, 5, 9, 0, time.Local)

	for _, tc := range []struct{ pattern, want string }{
		{"yyyyMMddHHmmss", "20260818140509"},
		{"yyyyMMdd", "20260818"},
		{"MM/dd/yyyy", "08/18/2026"},
		{"yyyy-MM-dd", "2026-08-18"},
		{"yyyy-MM-dd'T'HH:mm:ss", "2026-08-18T14:05:09"},
		{"dd-MMM-yyyy", "18-Aug-2026"},
		{"hh:mm a", "02:05 PM"},
		{"EEE, dd MMM yyyy", "Tue, 18 Aug 2026"},
		{"yy", "26"},
	} {
		t.Run(tc.pattern, func(t *testing.T) {
			if got := formatJavaDate(when, tc.pattern); got != tc.want {
				t.Errorf("format(%q) = %q, want %q", tc.pattern, got, tc.want)
			}
		})
	}
}

func TestJavaDateParsing(t *testing.T) {
	for _, tc := range []struct{ text, pattern, want string }{
		{"20260818", "yyyyMMdd", "2026-08-18"},
		{"08/18/2026", "MM/dd/yyyy", "2026-08-18"},
		// An HL7 timestamp longer than the pattern is truncated, matching
		// SimpleDateFormat, rather than rejected.
		{"20260818140509", "yyyyMMdd", "2026-08-18"},
	} {
		t.Run(tc.text, func(t *testing.T) {
			got, err := parseJavaDate(tc.text, tc.pattern)
			if err != nil {
				t.Fatal(err)
			}
			if got.Format("2006-01-02") != tc.want {
				t.Errorf("parsed %q as %s, want %s", tc.text, got.Format("2006-01-02"), tc.want)
			}
		})
	}

	if _, err := parseJavaDate("not a date", "yyyyMMdd"); err == nil {
		t.Error("an unparseable date should be an error, not a zero time")
	}
}

// TestUnconvertibleDatePatternIsRefused checks we do not guess. A silently wrong
// timestamp is accepted downstream and then misread.
func TestUnconvertibleDatePatternIsRefused(t *testing.T) {
	if _, err := javaToGoLayout("yyyy-ww"); err == nil {
		t.Error("week-of-year has no Go equivalent and should be refused")
	}
	if _, err := javaToGoLayout("GG yyyy"); err == nil {
		t.Error("era has no Go equivalent and should be refused")
	}
}

// TestCompileReportsE4XNotes checks the interface can tell somebody what was
// translated in their script.
func TestCompileReportsE4XNotes(t *testing.T) {
	e := newTestEngine(t)
	s, err := e.Compile("noted", `for each (var o in msg..OBX) { var v = o.@id; }`, Transformer)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, n := range s.Notes {
		kinds[n.Kind] = true
	}
	for _, want := range []string{"for-each", "descendant", "attribute"} {
		if !kinds[want] {
			t.Errorf("expected a %q note, got %+v", want, s.Notes)
		}
	}
}

// TestSyntaxErrorPointsAtTheRightLine matters because the script is rewritten
// before it runs, and a line number that refers to the rewritten form would send
// somebody to the wrong place.
func TestSyntaxErrorPointsAtTheRightLine(t *testing.T) {
	e := newTestEngine(t)
	_, err := e.Compile("broken", "var a = 1;\nvar b = ;\nvar c = 3;", Transformer)
	if err == nil {
		t.Fatal("expected a compile error")
	}
	if !strings.Contains(err.Error(), "2") {
		t.Errorf("error should point at line 2, got: %v", err)
	}
}

func TestConcurrentRunsAreSafe(t *testing.T) {
	e := New(Options{Timeout: 2 * time.Second, MaxVMs: 4})
	s, err := e.Compile("c",
		`globalMap.put('n', msg['PID']['PID.3']['PID.3.1'].toString());
		 channelMap.put('mine', msg['MSH']['MSH.10']['MSH.10.1'].toString());`, Transformer)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 20)
	for i := 0; i < 20; i++ {
		go func() {
			root, err := hl7xml.FromRaw([]byte(adt))
			if err != nil {
				done <- err
				return
			}
			_, err = e.Run(s, &Context{
				Message:     root,
				ChannelName: "c",
				ChannelMap:  NewSharedMap(),
			})
			done <- err
		}()
	}
	for i := 0; i < 20; i++ {
		if err := <-done; err != nil {
			t.Fatalf("concurrent run failed: %v", err)
		}
	}
}
