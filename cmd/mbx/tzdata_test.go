// ABOUTME: Verifies that the shipped mbx binary embeds the IANA time zone database.
// ABOUTME: Event time zones must load on systems without zoneinfo files, such as Windows.
package main

import (
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

// time.LoadLocation reads the system zone files first, so on a test machine that has them
// a load test passes with or without the embedded database. Check the import instead.
func TestMainEmbedsTimeZoneDatabase(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			t.Fatal(err)
		}
		if path == "time/tzdata" {
			return
		}
	}
	t.Fatal(`main.go does not import "time/tzdata"; event time zones fail where the system has no zone files`)
}
