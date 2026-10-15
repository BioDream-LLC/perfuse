package fhirpath

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

const patient = `{"resourceType":"Patient","id":"pt1","active":true,"birthDate":"1974-12-25",
 "name":[{"use":"official","family":"Doe","given":["Jane","Q"]},{"use":"maiden","family":"Smith","given":["Jane"]}],
 "telecom":[{"system":"phone","value":"555-0100"},{"system":"email","value":"jane@example.org"}],
 "deceasedBoolean":false,"multipleBirthInteger":2,
 "extension":[{"url":"http://example.org/race","valueCoding":{"code":"2106-3"}}]}`

func load(t *testing.T, s string) Collection {
	t.Helper()
	var m map[string]any
	d := json.NewDecoder(bytes.NewReader([]byte(s)))
	d.UseNumber()
	if err := d.Decode(&m); err != nil {
		t.Fatal(err)
	}
	return Collection{FromJSON(m, "")}
}

func show(c Collection) string {
	var p []string
	for _, it := range c {
		if s, ok := stringOf(it); ok {
			p = append(p, s)
		} else {
			b, _ := json.Marshal(ToJSON(it))
			p = append(p, string(b))
		}
	}
	return "[" + strings.Join(p, ", ") + "]"
}

func TestExpressions(t *testing.T) {
	in := load(t, patient)
	cases := []struct{ expr, want string }{
		{"name.family", "[Doe, Smith]"},
		{"Patient.name.given", "[Jane, Q, Jane]"},
		{"name.given.distinct()", "[Jane, Q]"},
		{"name.where(use = 'official').given.first()", "[Jane]"},
		{"name[1].family", "[Smith]"},
		{"name.count()", "[2]"},
		{"name.given.join(' ')", "[Jane Q Jane]"},
		{"telecom.where(system='email').value", "[jane@example.org]"},
		{"deceased", "[false]"},
		{"deceased.ofType(boolean)", "[false]"},
		{"deceased is boolean", "[true]"},
		{"multipleBirth.ofType(integer) + 1", "[3]"},
		{"1 + 2 * 3", "[7]"},
		{"(1 + 2) * 3", "[9]"},
		{"7 / 2", "[3.5]"},
		{"7 div 2", "[3]"},
		{"7 mod 2", "[1]"},
		{"0.1 + 0.2 = 0.3", "[true]"},
		{"1.0 = 1", "[true]"},
		{"'a' & {} & 'b'", "[ab]"},
		{"'abc'.upper().length()", "[3]"},
		{"'a,b,c'.split(',').count()", "[3]"},
		{"'hello'.substring(1, 3)", "[ell]"},
		{"'hello'.matches('^h.*o$')", "[true]"},
		{"'Hello' ~ 'hello'", "[true]"},
		{"birthDate < @1980-01-01", "[true]"},
		{"birthDate = @1974-12-25", "[true]"},
		{"@2020-01 = @2020-01-15", "[]"},
		{"@2020-01-31 + 1 month", "[2020-02-29]"},
		{"@2019-12-25 + 7 days", "[2020-01-01]"},
		{"@2012-04-15T10:00:00Z < @2012-04-15T09:00:00-02:00", "[true]"},
		{"true and {}", "[]"},
		{"false and {}", "[false]"},
		{"true or {}", "[true]"},
		{"{} implies false", "[]"},
		{"false implies {}", "[true]"},
		{"(1 | 2 | 2).count()", "[2]"},
		{"2 in (1 | 2)", "[true]"},
		{"name.exists(use = 'maiden')", "[true]"},
		{"name.all(family.exists())", "[true]"},
		{"iif(active, 'yes', 'no')", "[yes]"},
		{"extension('http://example.org/race').value.code", "[2106-3]"},
		{"name.select(given.first())", "[Jane, Jane]"},
		{"telecom.value.where($this.startsWith('555'))", "[555-0100]"},
		{"(3 | 1 | 2).where($index > 0).count()", "[2]"},
		{"1.0.lowBoundary()", "[0.95]"},
		{"1.587.highBoundary(2)", "[1.59]"},
		{"@2014.highBoundary()", "[2014-12-31]"},
		{"'12'.toInteger() + 1", "[13]"},
		{"'1.50'.toDecimal()", "[1.50]"},
		{"5 'mg' = 5 'mg'", "[true]"},
		{"(-5).abs()", "[5]"},
		{"2.5.round()", "[3]"},
		{"16.sqrt()", "[4]"},
		{"name.tail().family", "[Smith]"},
		{"name.skip(1).take(1).family", "[Smith]"},
		{"{}.empty()", "[true]"},
		{"telecom.system.union(name.use)", "[phone, email, official, maiden]"},
		{"telecom.system.combine(telecom.system).count()", "[4]"},
		{"name.given.intersect('Jane')", "[Jane]"},
		{"name.given.exclude('Jane')", "[Q]"},
		{"children().count() > 5", "[true]"},
	}
	for _, c := range cases {
		got, err := Eval(c.expr, in, nil)
		if err != nil {
			t.Errorf("%s: %v", c.expr, err)
			continue
		}
		if show(got) != c.want {
			t.Errorf("%s = %s, want %s", c.expr, show(got), c.want)
		}
	}
}

func TestErrors(t *testing.T) {
	in := load(t, patient)
	for _, e := range []string{"@@", "name.(", "name.given.substring()", "1 +", "'unterminated", "name.family < 1",
		"%undefined", "nosuchfunction()", "''.matches()", "(1|2) = 1 and name.family.single()"} {
		if _, err := Eval(e, in, nil); err == nil {
			t.Errorf("%s: no error", e)
		}
	}
}

func FuzzEval(f *testing.F) {
	for _, s := range []string{"name.where(use = 'official').given.first()", "1 + 2 * 3", "@2020-01-31 + 1 month",
		"iif(active, 'a', 'b')", "'x'.matches('[a-z]+')", "(1 | 2).select($this * 2)", "1.587.highBoundary(2)"} {
		f.Add(s)
	}
	var m map[string]any
	d := json.NewDecoder(strings.NewReader(patient))
	d.UseNumber()
	_ = d.Decode(&m)
	in := Collection{FromJSON(m, "")}
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 300 || strings.Contains(s, "repeat") || strings.Contains(s, "descendants") {
			return
		}
		_, _ = Eval(s, in, nil)
	})
}
