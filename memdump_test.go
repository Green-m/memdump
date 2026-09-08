package main

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"regexp"
	"strings"
	"testing"
)

func TestParseOptionsHelp(t *testing.T) {
	_, err := parseOptions([]string{"-h"})
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("error = %v, want flag.ErrHelp", err)
	}
}

func TestParseOptionsScanModes(t *testing.T) {
	stringsOpts, err := parseOptions([]string{"-strings", "-address", "123", "-"})
	if err != nil {
		t.Fatal(err)
	}
	if stringsOpts.mode != modeStrings || stringsOpts.mapPath != "-" || !stringsOpts.showAddress {
		t.Fatalf("unexpected strings options: %+v", stringsOpts)
	}

	regexpOpts, err := parseOptions([]string{"-regex", `token=[[:alnum:]]+`, "123", "matches.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if regexpOpts.mode != modeRegexp || regexpOpts.regexpText != `token=[[:alnum:]]+` || regexpOpts.showAddress {
		t.Fatalf("unexpected regexp options: %+v", regexpOpts)
	}

	if _, err := parseOptions([]string{"-strings", "-regex", "token", "123", "-"}); err == nil {
		t.Fatal("expected mutually exclusive mode error")
	}
	if _, err := parseOptions([]string{"-regex", "[", "123", "-"}); err == nil {
		t.Fatal("expected invalid regexp error")
	}
	if _, err := parseOptions([]string{"-address", "123", "dump.bin"}); err == nil {
		t.Fatal("expected address mode error")
	}
}

func TestParseMappingLine(t *testing.T) {
	line := "7f1234500000-7f1234521000 rw-p 00000000 00:00 0 /tmp/a file (deleted)"
	m, err := parseMappingLine(line)
	if err != nil {
		t.Fatal(err)
	}
	if m.start != 0x7f1234500000 || m.end != 0x7f1234521000 {
		t.Fatalf("unexpected range: %#x-%#x", m.start, m.end)
	}
	if m.permissions != "rw-p" || m.pathname != "/tmp/a file (deleted)" {
		t.Fatalf("unexpected mapping: %+v", m)
	}
}

func TestParseMappingsFilters(t *testing.T) {
	maps := strings.NewReader(`00400000-00401000 r--p 00000000 08:01 1 /usr/bin/demo
00401000-00402000 ---p 00001000 08:01 1 /usr/bin/demo
00600000-00601000 rw-p 00000000 00:00 0
7fff00000000-7fff00001000 rw-p 00000000 00:00 0 [stack]
`)
	all, err := parseMappings(maps, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("got %d readable mappings, want 3", len(all))
	}

	maps = strings.NewReader(`00400000-00401000 r--p 00000000 08:01 1 /usr/bin/demo
00600000-00601000 rw-p 00000000 00:00 0
7fff00000000-7fff00001000 rw-p 00000000 00:00 0 [stack]
`)
	anonymous, err := parseMappings(maps, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(anonymous) != 2 {
		t.Fatalf("got %d anonymous mappings, want 2", len(anonymous))
	}
}

func TestDumpMappingFallsBackToPages(t *testing.T) {
	reader := &selectiveReaderAt{data: []byte("abcdefghijklmnop"), badStart: 8, badEnd: 12}
	var output bytes.Buffer
	m := mapping{start: 0, end: 16, permissions: "r--p"}
	unreadable, err := dumpMapping(reader, &output, m, make([]byte, 16), 4, false)
	if err != nil {
		t.Fatal(err)
	}
	if unreadable != 4 {
		t.Fatalf("unreadable = %d, want 4", unreadable)
	}
	want := append([]byte("abcdefgh"), []byte{0, 0, 0, 0}...)
	want = append(want, []byte("mnop")...)
	if !bytes.Equal(output.Bytes(), want) {
		t.Fatalf("output = %v, want %v", output.Bytes(), want)
	}
}

func TestDumpMappingStrict(t *testing.T) {
	reader := &selectiveReaderAt{data: []byte("abcdefgh"), badStart: 4, badEnd: 8}
	var output bytes.Buffer
	_, err := dumpMapping(reader, &output, mapping{start: 0, end: 8}, make([]byte, 8), 4, true)
	if err == nil {
		t.Fatal("expected strict read error")
	}
}

func TestDumpMappingSupportsUnsignedAddresses(t *testing.T) {
	const start = uint64(0xffffffffff600000)
	reader := &unsignedAddressReaderAt{
		start: start,
		data:  []byte("vsyscall"),
	}
	var output bytes.Buffer
	m := mapping{start: start, end: start + uint64(len(reader.data)), permissions: "r-xp", pathname: "[vsyscall]"}

	unreadable, err := dumpMapping(reader, &output, m, make([]byte, len(reader.data)), 4096, false)
	if err != nil {
		t.Fatal(err)
	}
	if unreadable != 0 {
		t.Fatalf("unreadable = %d, want 0", unreadable)
	}
	if !bytes.Equal(output.Bytes(), reader.data) {
		t.Fatalf("output = %q, want %q", output.Bytes(), reader.data)
	}
}

func TestPrintableStringCollectorAcrossChunks(t *testing.T) {
	var output bytes.Buffer
	collector := printableStringCollector{
		output:    &output,
		minLength: 4,
		matches:   func([]byte) bool { return true },
	}
	if err := collector.consume(0x1000, []byte{'x', 'x', 0, 's', 'e'}); err != nil {
		t.Fatal(err)
	}
	if err := collector.consume(0x1005, []byte("cret-value\x00end")); err != nil {
		t.Fatal(err)
	}
	if err := collector.finish(); err != nil {
		t.Fatal(err)
	}

	want := "secret-value\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
	if collector.matchCount != 1 {
		t.Fatalf("match count = %d, want 1", collector.matchCount)
	}
}

func TestPrintableStringCollectorRegexp(t *testing.T) {
	var output bytes.Buffer
	re := regexp.MustCompile(`token=[[:alnum:]]+`)
	collector := printableStringCollector{
		output:    &output,
		minLength: 4,
		matches:   re.Match,
	}
	if err := collector.consume(0x2000, []byte("ordinary text\x00prefix token=abc123 suffix\x00tiny\x00")); err != nil {
		t.Fatal(err)
	}

	want := "prefix token=abc123 suffix\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
	if collector.matchCount != 1 {
		t.Fatalf("match count = %d, want 1", collector.matchCount)
	}
}

func TestPrintableStringCollectorWithAddress(t *testing.T) {
	var output bytes.Buffer
	collector := printableStringCollector{
		output:      &output,
		minLength:   4,
		matches:     func([]byte) bool { return true },
		showAddress: true,
	}
	if err := collector.consume(0x3000, []byte("value\x00")); err != nil {
		t.Fatal(err)
	}

	want := "0000000000003000 value\n"
	if output.String() != want {
		t.Fatalf("output = %q, want %q", output.String(), want)
	}
}

type selectiveReaderAt struct {
	data             []byte
	badStart, badEnd int64
}

type unsignedAddressReaderAt struct {
	start uint64
	data  []byte
}

func (r *unsignedAddressReaderAt) ReadAt(p []byte, off int64) (int, error) {
	address := uint64(off)
	if address < r.start || address-r.start >= uint64(len(r.data)) {
		return 0, io.EOF
	}
	n := copy(p, r.data[address-r.start:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (r *selectiveReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off < r.badEnd && off+int64(len(p)) > r.badStart {
		if off >= r.badStart && off < r.badEnd {
			return 0, errors.New("unreadable")
		}
		limit := r.badStart - off
		copy(p[:limit], r.data[off:r.badStart])
		return int(limit), errors.New("unreadable")
	}
	if off >= int64(len(r.data)) {
		return 0, io.EOF
	}
	n := copy(p, r.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}
