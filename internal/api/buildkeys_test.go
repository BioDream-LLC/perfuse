package api

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Guards against the browser sending a key the build endpoint cannot read.
//
// # What happened
//
// The DICOM query source was written as `source.dicom_query = {...}` in the browser, which is the file format's spelling. The build
// endpoint decodes JSON strictly and its field is `dicomQuery`, so every build with that source type was refused outright - "unknown
// field dicom_query" - and the source could be chosen in the form while no channel could ever be produced from it. Two of its inner
// keys, called_ae and calling_ae, were wrong the same way.
//
// It survived because nothing else in the form could set those fields, so no test exercised the path, and because the failure is a
// four hundred on the whole request rather than a missing value - which reads as "the builder is broken" rather than "this source type
// is broken", and nobody had reason to try that source type.
//
// # Why a source-reading test
//
// The alternative is driving the form through every source and destination type, which is the right long-term answer and is a much
// larger test. This is cheap, and it catches the exact mistake: a snake_case key in a JSON body. The convention in this project is
// stated in one line - the server writes YAML in snake_case, the form sends JSON in camelCase - and this asserts the second half.

// jsonBodyKeys finds object keys written in the request-building source.
//
// Deliberately narrow: a key at the start of a line, lowercase, containing an underscore. That is the shape of the mistake. A broader
// scan would match YAML strings, comments and sample data, and a guard that reports those is one somebody switches off.
var jsonBodyKeys = regexp.MustCompile(`(?m)^\s+([a-z][a-zA-Z0-9]*_[a-zA-Z0-9_]+):`)

// requestBuilders are the files that assemble a request body for the build endpoint.
var requestBuilders = []string{"model.ts"}

func TestTheFormSendsNoSnakeCaseKeys(t *testing.T) {
	root := filepath.Join("..", "..", "web", "src")

	var read int

	for _, name := range requestBuilders {
		body, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}

		read++

		for _, m := range jsonBodyKeys.FindAllStringSubmatch(string(body), -1) {
			key := m[1]

			if allowedSnakeCaseKeys[key] {
				continue
			}

			t.Errorf("%s sends %q, which is snake_case. The build endpoint decodes JSON strictly and its fields are camelCase, "+
				"so this key is refused and the whole request fails - which reads as a broken builder rather than a broken "+
				"field. Write it as the Go tag spells it.", name, key)
		}
	}

	if read != len(requestBuilders) {
		t.Fatalf("read %d of %d request builders, so this test is not looking at what it thinks it is", read,
			len(requestBuilders))
	}
}

// allowedSnakeCaseKeys are keys that really are snake_case on the wire.
//
// Empty, and worth keeping empty. An entry here is a place where the request body disagrees with the convention, and the convention
// existing is what makes the rest of this checkable.
var allowedSnakeCaseKeys = map[string]bool{}

// TestEveryBuildModelBlockIsNamedAsTheFormSpellsIt pairs the per-kind block names both ways.
//
// The snake_case check above catches the shape of the mistake. This catches the rest of it: a block the browser names in camelCase
// that still does not match its Go tag, which no amount of casing discipline would reveal.
func TestEveryBuildModelBlockIsNamedAsTheFormSpellsIt(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "model.ts"))
	if err != nil {
		t.Fatal(err)
	}

	source := string(body)

	// Every per-kind block on a source, from the Go model.
	var missing []string

	srcType := reflect.TypeOf(buildSource{})

	for i := 0; i < srcType.NumField(); i++ {
		f := srcType.Field(i)

		tag := strings.Split(f.Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" || f.Type.Kind() != reflect.Ptr {
			continue
		}

		// The form assigns the block by name. Anything it never mentions is either unreachable from the builder - which the field
		// guard reports separately - or spelled differently, which is this test's subject.
		if !strings.Contains(source, "source."+tag) {
			missing = append(missing, tag)
		}
	}

	sort.Strings(missing)

	for _, tag := range missing {
		if unreachableSourceBlocks[tag] {
			continue
		}

		t.Errorf("the build model has a source block %q and model.ts never assigns source.%s, so either the form cannot produce "+
			"that source type or it spells the key differently - and a different spelling is refused as an unknown field, "+
			"failing the whole request", tag, tag)
	}
}

// unreachableSourceBlocks are source kinds the builder deliberately does not offer.
//
// Separate from a spelling mistake, and kept short. Each entry says the builder cannot create this source at all, which is a product
// decision rather than a naming one.
var unreachableSourceBlocks = map[string]bool{}
