package main

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"strings"
	"testing"
)

func TestParseOptionsHelp(t *testing.T) {
	_, err := parseOptions([]string{"-h"})
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("error = %v, want flag.ErrHelp", err)
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

type selectiveReaderAt struct {
	data             []byte
	badStart, badEnd int64
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
