// Command xmlordergen writes the element order FHIR XML needs, from a core package's StructureDefinitions.
//
//	go run ./fhir/xmlordergen hl7.fhir.r4.core-4.0.1.tgz | gzip -9 -n > fhir/xmlorder_r4.txt.gz
//
// JSON objects may hold their members in any order; XML may not: every element must come in the order its
// definition lists. Each output line is a path and its children in that order, each child with how to find its own
// children: "@Path" (an element defined inline, a backbone), "#Path" (the same as another element), or its types.
package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

type structureDefinition struct {
	ResourceType string `json:"resourceType"`
	Kind         string `json:"kind"`
	Derivation   string `json:"derivation"`
	Abstract     bool   `json:"abstract"`
	Type         string `json:"type"`
	Snapshot     struct {
		Element []struct {
			Path             string `json:"path"`
			ContentReference string `json:"contentReference"`
			Type             []struct {
				Code string `json:"code"`
			} `json:"type"`
		} `json:"element"`
	} `json:"snapshot"`
}

func main() {
	f, err := os.Open(os.Args[1])
	check(err)
	gz, err := gzip.NewReader(f)
	check(err)
	tr := tar.NewReader(gz)

	children := map[string][]string{}
	var order []string

	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		check(err)
		if !strings.HasPrefix(h.Name, "package/StructureDefinition-") || !strings.HasSuffix(h.Name, ".json") {
			continue
		}
		var sd structureDefinition
		if json.NewDecoder(tr).Decode(&sd) != nil || sd.ResourceType != "StructureDefinition" {
			continue
		}
		if sd.Derivation != "specialization" || (sd.Kind != "resource" && sd.Kind != "complex-type") {
			continue
		}
		for _, e := range sd.Snapshot.Element {
			i := strings.LastIndexByte(e.Path, '.')
			if i < 0 {
				continue
			}
			parent, name := e.Path[:i], e.Path[i+1:]
			var how string
			switch {
			case e.ContentReference != "":
				how = "#" + strings.TrimPrefix(e.ContentReference[strings.IndexByte(e.ContentReference, '#')+1:], "#")
			case len(e.Type) == 1 && (e.Type[0].Code == "BackboneElement" || e.Type[0].Code == "Element"):
				how = "@" + e.Path
			default:
				var codes []string
				for _, t := range e.Type {
					codes = append(codes, strings.TrimPrefix(t.Code, "http://hl7.org/fhirpath/System."))
				}
				how = strings.Join(codes, ",")
			}
			if _, seen := children[parent]; !seen {
				order = append(order, parent)
			}
			children[parent] = append(children[parent], name+":"+how)
		}
	}
	sort.Strings(order)
	for _, p := range order {
		fmt.Printf("%s\t%s\n", p, strings.Join(children[p], " "))
	}
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
