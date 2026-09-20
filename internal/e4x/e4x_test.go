package e4x

import (
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/hl7xml"
	"github.com/dop251/goja"
)

const sample = "MSH|^~\\&|SENDAPP|SITEA|RECV|RFAC|20260818120000||ADT^A01^ADT_A01|CTRL1|P|2.5.1\r" +
	"EVN|A01|20260818115900\r" +
	"PID|1||MRN9^^^SITEA^MR~999^^^SSA^SS||Doe^Jane^Q^^Ms.||19800101|F\r" +
	"PV1|1|I|ICU^7^01^SITEA||||1234^Smith^Sam\r" +
	"OBX|1|NM|718-7^Hemoglobin^LN||13.5|g/dL|12.0-16.0|N|||F\r" +
	"OBX|2|NM|6690-2^Leukocytes^LN||14.2|10*3/uL|4.0-11.0|H|||F\r"

// run evaluates a script with msg bound to the sample message, the way a Mirth
// transformer sees it.
func run(t *testing.T, script string) (goja.Value, string) {
	t.Helper()

	root, err := hl7xml.FromRaw([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}

	vm := goja.New()
	rt, err := NewRuntime(vm)
	if err != nil {
		t.Fatal(err)
	}
	if err := vm.Set("msg", rt.Wrap(root)); err != nil {
		t.Fatal(err)
	}

	rewritten, _, err := Preprocess(script)
	if err != nil {
		t.Fatalf("preprocessing failed: %v", err)
	}

	v, err := vm.RunString(rewritten)
	if err != nil {
		t.Fatalf("script failed: %v\nrewritten:\n%s", err, rewritten)
	}

	out, err := hl7xml.ToER7(root, hl7xml.DefaultOptions())
	if err != nil {
		return v, ""
	}
	return v, string(out)
}

// TestRealMirthIdioms is the test that decides whether the compatibility claim
// is true. Every script here is written the way Mirth documentation and real
// channels write it, and none of it is adapted for this engine.
func TestRealMirthIdioms(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script string
		want   string
	}{
		{
			"read a component",
			`msg['PID']['PID.5']['PID.5.1'].toString()`,
			"Doe",
		},
		{
			"read the message type",
			`msg['MSH']['MSH.9']['MSH.9.2'].toString()`,
			"A01",
		},
		{
			// E4X's toString on complex content returns markup, not text. This is
			// faithful rather than helpful on purpose: a script that logs a whole
			// segment expects to see XML, and Mirth users write the trailing .1
			// for exactly this reason.
			"complex content stringifies as markup, as E4X does",
			`msg.PID['PID.8'].toString()`,
			"<PID.8><PID.8.1>F</PID.8.1></PID.8>",
		},
		{
			"dot access on a segment, reading the component",
			`msg.PID['PID.8']['PID.8.1'].toString()`,
			"F",
		},
		{
			"count repetitions",
			`msg['PID']['PID.3'].length().toString()`,
			"2",
		},
		{
			"index a repetition",
			`msg['PID']['PID.3'][1]['PID.3.5'].toString()`,
			"SS",
		},
		{
			"count segments",
			`msg['OBX'].length().toString()`,
			"2",
		},
		{
			"missing field is empty, not an error",
			`msg['PID']['PID.99']['PID.99.1'].toString()`,
			"",
		},
		{
			"missing segment reads as empty",
			`msg['ZZZ']['ZZZ.1'].toString()`,
			"",
		},
		{
			"string comparison in a condition",
			`msg['PV1']['PV1.2']['PV1.2.1'].toString() == 'I' ? 'inpatient' : 'other'`,
			"inpatient",
		},
		{
			"implicit string method on an XML value",
			`msg['PID']['PID.5']['PID.5.1'].toUpperCase()`,
			"DOE",
		},
		{
			"indexOf on an XML value",
			`msg['PID']['PID.5']['PID.5.1'].indexOf('o').toString()`,
			"1",
		},
		{
			"text of a whole field",
			`msg['PID']['PID.5'].text()`,
			"DoeJaneQMs.",
		},
		{
			"name of an element",
			`msg['PID'].name()`,
			"PID",
		},
		{
			"parent of a segment",
			`msg['PID'].parent().name()`,
			"HL7Message",
		},
		{
			"for-each over segments",
			`var out = ''; for each (var obx in msg['OBX']) { out += obx['OBX.3']['OBX.3.2'].toString() + ';'; } out`,
			"Hemoglobin;Leukocytes;",
		},
		{
			"for-each with the descendant operator",
			`var n = 0; for each (var o in msg..OBX) { n++; } n.toString()`,
			"2",
		},
		{
			"descendant operator selecting deep elements",
			`msg..['OBX.5'].length().toString()`,
			"2",
		},
		{
			"for-each over repetitions collecting identifiers",
			`var ids = []; for each (var id in msg['PID']['PID.3']) { ids.push(id['PID.3.1'].toString()); } ids.join(',')`,
			"MRN9,999",
		},
		{
			"for-in over a list gives indexes",
			`var keys = []; for (var i in msg['OBX']) { keys.push(i); } keys.join(',')`,
			"0,1",
		},
		{
			"nested conditional on an abnormal flag",
			`var abn = 0; for each (var o in msg['OBX']) { if (o['OBX.8']['OBX.8.1'].toString() == 'H') abn++; } abn.toString()`,
			"1",
		},
		{
			"hasSimpleContent",
			`msg['PID']['PID.5']['PID.5.1'].hasSimpleContent().toString()`,
			"true",
		},
		{
			"children of a segment",
			`msg['PID'].children().length().toString()`,
			"9",
		},
		{
			"regular expression still works",
			`msg['PID']['PID.7']['PID.7.1'].toString().replace(/(\d{4})(\d{2})(\d{2})/, '$1-$2-$3')`,
			"1980-01-01",
		},
		{
			"comparison operators are not mistaken for XML",
			`var a = 5, b = 3; (a < b ? 'lt' : 'ge') + (a > b ? '-gt' : '-le')`,
			"ge-gt",
		},
		{
			"a URL in a string is not a descendant operator",
			`var u = 'http://example.org/a..b'; u.length.toString()`,
			"23",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := run(t, tc.script)
			if got.String() != tc.want {
				t.Errorf("got %q, want %q", got.String(), tc.want)
			}
		})
	}
}

// TestMirthTransformations checks that the assignments a transformer makes reach
// the wire, since a script that appears to work but does not change the outgoing
// message is the worst possible outcome.
func TestMirthTransformations(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script string
		expect string
		absent string
	}{
		{
			name:   "overwrite a component",
			script: `msg['PID']['PID.5']['PID.5.1'] = 'Smith';`,
			expect: "|Smith^Jane^Q^^Ms.|",
		},
		{
			name:   "set a field that was absent",
			script: `msg['PID']['PID.11']['PID.11.1'] = '1 Main St';`,
			expect: "1 Main St",
		},
		{
			name:   "copy one field to another",
			script: `msg['PID']['PID.18']['PID.18.1'] = msg['PV1']['PV1.19']['PV1.19.1'].toString();`,
			expect: "PID|",
		},
		{
			name:   "clear a field",
			script: `msg['PID']['PID.7']['PID.7.1'] = '';`,
			absent: "19800101",
		},
		{
			name:   "delete a segment",
			script: `delete msg['EVN'];`,
			absent: "EVN|",
		},
		{
			name:   "append a Z segment from an XML literal",
			script: `msg.appendChild(<ZPD><ZPD.1><ZPD.1.1>local</ZPD.1.1></ZPD.1></ZPD>);`,
			expect: "ZPD|local",
		},
		{
			name:   "append a segment built with XML()",
			script: `msg.appendChild(new XML('<ZQQ><ZQQ.1><ZQQ.1.1>x</ZQQ.1.1></ZQQ.1></ZQQ>'));`,
			expect: "ZQQ|x",
		},
		{
			name:   "XML literal with interpolation",
			script: `var v = 'interp'; msg.appendChild(<ZIN><ZIN.1><ZIN.1.1>{v}</ZIN.1.1></ZIN.1></ZIN>);`,
			expect: "ZIN|interp",
		},
		{
			name:   "modify every repetition in a loop",
			script: `for each (var id in msg['PID']['PID.3']) { id['PID.3.4']['PID.3.4.1'] = 'NEWFAC'; }`,
			expect: "MRN9^^^NEWFAC^MR~999^^^NEWFAC^SS",
		},
		{
			name:   "conditional transformation across segments",
			script: `for each (var o in msg['OBX']) { if (o['OBX.8']['OBX.8.1'].toString() == 'H') { o['OBX.8']['OBX.8.1'] = 'HH'; } }`,
			expect: "|HH|",
		},
		{
			name:   "set the message type",
			script: `msg['MSH']['MSH.9']['MSH.9.2'] = 'A08';`,
			expect: "ADT^A08^ADT_A01",
		},
		{
			name:   "build a new repetition past the end",
			script: `msg['PID']['PID.3'][2]['PID.3.1'] = 'THIRD';`,
			expect: "~THIRD",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, out := run(t, tc.script)
			if tc.expect != "" && !strings.Contains(out, tc.expect) {
				t.Errorf("output missing %q:\n%s", tc.expect, out)
			}
			if tc.absent != "" && strings.Contains(out, tc.absent) {
				t.Errorf("output should no longer contain %q:\n%s", tc.absent, out)
			}
		})
	}
}

// TestPreprocessorLeavesOrdinaryCodeAlone is the safety property. A rewriter that
// corrupts a working script is worse than one that refuses to run it, so anything
// without E4X must come through byte for byte.
func TestPreprocessorLeavesOrdinaryCodeAlone(t *testing.T) {
	for _, src := range []string{
		`var a = 1 < 2;`,
		`if (a<b && c>d) { x(); }`,
		`var re = /a..b/g; 'aXXb'.replace(re, 'y');`,
		`var s = "a..b"; var t = 'x.@y';`,
		`var url = "http://example.org";`,
		`// a comment with .. and .@ and <foo/>`,
		`/* block .. .@ <foo/> */ var x = 1;`,
		`var t = ` + "`" + `template with ${a} and .. and <b/>` + "`" + `;`,
		`f(...args);`,
		`x = a / b / c;`,
		`var n = 1..toString();`,
		`for (var i = 0; i < 10; i++) {}`,
		`obj.map(function(x) { return x < 5; });`,
		`var cmp = a <= b || c >= d;`,
		`x <<= 2;`,
	} {
		t.Run(src, func(t *testing.T) {
			out, notes, err := Preprocess(src)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if out != src {
				t.Errorf("ordinary code was rewritten:\n in: %s\nout: %s", src, out)
			}
			if len(notes) != 0 {
				t.Errorf("ordinary code produced %d notes: %+v", len(notes), notes)
			}
		})
	}
}

// TestPreprocessorRewrites pins the shape of each rewrite so that a change to it
// is a deliberate decision rather than a surprise.
func TestPreprocessorRewrites(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{
			"for each with var",
			`for each (var s in msg.OBX) { }`,
			`for (var s of __e4xEach(msg.OBX)) { }`,
		},
		{
			"for each without a declaration",
			`for each (s in list) { }`,
			`for (s of __e4xEach(list)) { }`,
		},
		{
			"descendant operator",
			`var x = msg..OBX;`,
			`var x = msg.__e4xDescendants('OBX');`,
		},
		{
			"descendant wildcard",
			`var x = msg..*;`,
			`var x = msg.__e4xDescendants('*');`,
		},
		{
			"attribute reference",
			`var id = node.@root;`,
			`var id = node.__e4xAttribute('root');`,
		},
		{
			"attribute with a hyphen",
			`var v = n.@xsi-type;`,
			`var v = n.__e4xAttribute('xsi-type');`,
		},
		{
			"all attributes",
			`var a = node.@*;`,
			`var a = node.__e4xAttributes();`,
		},
		{
			"self closing literal",
			`var e = <br/>;`,
			"var e = __e4xParse(`<br/>`);",
		},
		{
			"literal with children",
			`var e = <a><b>c</b></a>;`,
			"var e = __e4xParse(`<a><b>c</b></a>`);",
		},
		{
			"literal with interpolation",
			`var e = <a>{v}</a>;`,
			"var e = __e4xParse(`<a>${v}</a>`);",
		},
		{
			"literal returned",
			`return <a/>;`,
			"return __e4xParse(`<a/>`);",
		},
		{
			"nested for each with descendant",
			`for each (var o in msg..OBX) { }`,
			`for (var o of __e4xEach(msg.__e4xDescendants('OBX'))) { }`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, notes, err := Preprocess(tc.in)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("\n in: %s\ngot: %s\nwant: %s", tc.in, got, tc.want)
			}
			if len(notes) == 0 {
				t.Error("a rewrite should be reported in the notes")
			}
		})
	}
}

// TestPreprocessorReportsUnterminated checks that broken input is refused with a
// line number rather than silently producing nonsense.
func TestPreprocessorReportsUnterminated(t *testing.T) {
	for _, src := range []string{
		"var s = 'unclosed;",
		"/* unclosed",
		"var x = <a><b></a>;",
	} {
		if _, _, err := Preprocess(src); err == nil {
			t.Errorf("expected an error for %q", src)
		}
	}
}

// TestLineNumbersSurvive matters for debugging: a script that fails on line 40
// must say line 40, and rewriting must not shift the count.
func TestLineNumbersSurvive(t *testing.T) {
	src := "var a = 1;\nvar b = 2;\nfor each (var s in list) {\n  s.x = 1;\n}\nvar c = 3;\n"
	out, _, err := Preprocess(src)
	if err != nil {
		t.Fatal(err)
	}
	if in, got := strings.Count(src, "\n"), strings.Count(out, "\n"); in != got {
		t.Errorf("line count changed from %d to %d, so error positions will be wrong", in, got)
	}
}
