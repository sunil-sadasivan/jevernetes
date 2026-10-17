package spdy

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"runtime"
	"testing"
)

func rawFrame(kind uint16, length uint32, payload []byte) []byte {
	var out bytes.Buffer
	binary.Write(&out, binary.BigEndian, uint32(0x80030000)|uint32(kind))
	binary.Write(&out, binary.BigEndian, length)
	out.Write(payload)
	return out.Bytes()
}

func TestOversizedDataAllocation(t *testing.T) {
	// Safe baseline witness: old decoder allocates 16 MiB for eight wire bytes.
	raw := []byte{0, 0, 0, 1, 0, 255, 255, 255}
	f, err := NewFramer(io.Discard, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err = f.ReadFrame()
	runtime.ReadMemStats(&after)
	if err == nil {
		t.Fatal("accepted oversized frame")
	}
	if n := after.TotalAlloc - before.TotalAlloc; n > 2<<20 {
		t.Fatalf("eight bytes allocated %d bytes", n)
	}
}

func TestSettingsWitness(t *testing.T) {
	raw := []byte{0x80, 3, 0, 4, 0, 0, 0, 4, 0x10, 0, 0, 0}
	f, err := NewFramer(io.Discard, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err = f.ReadFrame()
	runtime.ReadMemStats(&after)
	if err == nil {
		t.Fatal("accepted SETTINGS count without payload")
	}
	if n := after.TotalAlloc - before.TotalAlloc; n > 2<<20 {
		t.Fatalf("SETTINGS allocated %d", n)
	}
	// A failed decoder cannot resume at attacker-selected bytes.
	if _, again := f.ReadFrame(); again != err {
		t.Fatal("decoder did not fail closed")
	}
}

func TestFrameEnvelopes(t *testing.T) {
	for _, kind := range []uint16{1, 2, 3, 4, 5, 6, 7, 8, 9, 65535} {
		for _, length := range []uint32{0, 1, 3, 0xffffff} {
			t.Run(fmt.Sprintf("%d/%d", kind, length), func(t *testing.T) {
				f, _ := NewFramer(io.Discard, bytes.NewReader(rawFrame(kind, length, nil)))
				if _, err := f.ReadFrame(); err == nil {
					t.Fatal("accepted invalid envelope")
				}
			})
		}
	}
	for _, raw := range [][]byte{
		rawFrame(3, 9, make([]byte, 9)), rawFrame(6, 5, make([]byte, 5)),
		rawFrame(7, 9, make([]byte, 9)), rawFrame(9, 9, make([]byte, 9)),
		rawFrame(4, 12, []byte{0, 0, 0, 0}), // trailing SETTINGS payload
		rawFrame(4, 4+8*1025, []byte{0, 0, 4, 1}),
		rawFrame(1, 65547, nil), rawFrame(2, 65541, nil), rawFrame(8, 65541, nil),
		{0x80, 2, 0, 6, 0, 0, 0, 4, 0, 0, 0, 1}, // wrong version
		{0x80, 3, 0, 6, 2, 0, 0, 4, 0, 0, 0, 1}, // unknown flags
		{0, 0, 0, 1, 2, 0, 0, 0},                // unknown DATA flags
		{0, 0, 0, 0, 0, 0, 0, 0},                // zero DATA stream
	} {
		f, _ := NewFramer(io.Discard, bytes.NewReader(raw))
		if _, err := f.ReadFrame(); err == nil {
			t.Fatalf("accepted %x", raw[:8])
		}
	}
}

func headerWords(words ...uint32) []byte {
	var out bytes.Buffer
	for _, word := range words {
		binary.Write(&out, binary.BigEndian, word)
	}
	return out.Bytes()
}

func compressedFrame(t *testing.T, kind uint16, block []byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	z, err := zlib.NewWriterLevelDict(&compressed, zlib.BestSpeed, headerDictionary)
	if err != nil {
		t.Fatal(err)
	}
	z.Write(block)
	z.Flush()
	prefix := 4
	if kind == 1 {
		prefix = 10
	}
	payload := make([]byte, prefix)
	payload[3] = 1
	payload = append(payload, compressed.Bytes()...)
	z.Close()
	return rawFrame(kind, uint32(len(payload)), payload)
}

func TestCompressedHeaderAllocationBounds(t *testing.T) {
	// All three header-carrying frame types reach the same bounded parser.
	cases := map[string][]byte{
		"count": headerWords(0x10000000),
		"name":  headerWords(1, 0xffffffff),
		"value": append(append(headerWords(1, 1), 'a'), headerWords(0xffffffff)...),
		"bomb":  append(append(append(headerWords(1, 1), 'a'), headerWords(8<<20)...), bytes.Repeat([]byte{'x'}, 8<<20)...),
	}
	var aggregate bytes.Buffer
	aggregate.Write(headerWords(8))
	for i := 0; i < 8; i++ {
		aggregate.Write(headerWords(1))
		aggregate.WriteByte(byte('a' + i))
		aggregate.Write(headerWords(16000))
		aggregate.Write(bytes.Repeat([]byte{'x'}, 16000))
	}
	cases["aggregate"] = aggregate.Bytes()
	for name, block := range cases {
		for _, kind := range []uint16{1, 2, 8} {
			t.Run(fmt.Sprintf("%s/%d", name, kind), func(t *testing.T) {
				raw := compressedFrame(t, kind, block)
				f, _ := NewFramer(io.Discard, bytes.NewReader(raw))
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				_, err := f.ReadFrame()
				runtime.ReadMemStats(&after)
				if err == nil {
					t.Fatal("accepted malicious compressed headers")
				}
				if n := after.TotalAlloc - before.TotalAlloc; n > 2<<20 {
					t.Fatalf("header allocated %d", n)
				}
			})
		}
	}
}

func TestBoundedValidFramesAndCompressionContext(t *testing.T) {
	var raw bytes.Buffer
	writer, _ := NewFramer(&raw, nil)
	frames := []Frame{
		&SettingsFrame{FlagIdValues: []SettingsFlagIdValue{{Id: 1, Value: 2}}},
		&PingFrame{Id: 1}, &WindowUpdateFrame{StreamId: 1, DeltaWindowSize: 1024},
		&SynStreamFrame{StreamId: 1, Headers: http.Header{"X-Test": []string{"same context"}}},
		&SynReplyFrame{StreamId: 1, Headers: http.Header{"X-Test": []string{"same context"}}},
		&HeadersFrame{StreamId: 1, Headers: http.Header{"X-Test": []string{"same context"}}},
		&DataFrame{StreamId: 1, Data: bytes.Repeat([]byte{'d'}, 1<<20)},
		&RstStreamFrame{StreamId: 1, Status: 1}, &GoAwayFrame{LastGoodStreamId: 1},
	}
	for _, frame := range frames {
		if err := writer.WriteFrame(frame); err != nil {
			t.Fatal(err)
		}
	}
	reader, _ := NewFramer(io.Discard, &raw)
	for _, want := range frames {
		got, err := reader.ReadFrame()
		if err != nil {
			t.Fatalf("%T: %v", want, err)
		}
		if reflect.TypeOf(got) != reflect.TypeOf(want) {
			t.Fatalf("got %T want %T", got, want)
		}
	}
	if raw.Len() != 0 {
		t.Fatal("unconsumed frame bytes")
	}
}

func TestHeaderBudgetsAndValueSplits(t *testing.T) {
	block := func(name, value []byte) []byte {
		out := headerWords(1, uint32(len(name)))
		out = append(out, name...)
		out = append(out, headerWords(uint32(len(value)))...)
		return append(out, value...)
	}
	for _, tc := range []struct {
		name  string
		raw   []byte
		valid bool
	}{
		{"name-at-cap", block(bytes.Repeat([]byte{'a'}, 4096), nil), true},
		{"name-over-cap", block(bytes.Repeat([]byte{'a'}, 4097), nil), false},
		{"value-at-cap", block([]byte{'a'}, bytes.Repeat([]byte{'x'}, 16384)), true},
		{"value-over-cap", block([]byte{'a'}, bytes.Repeat([]byte{'x'}, 16385)), false},
		{"split-at-cap", block([]byte{'a'}, bytes.Repeat([]byte{0}, 255)), true},
		{"split-over-cap", block(bytes.Repeat([]byte{'a'}, 4096), bytes.Repeat([]byte{0}, 256)), false},
		{"count-over-cap", headerWords(257), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseHeaderValueBlock(bytes.NewReader(tc.raw), 1)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%t, error=%v", tc.valid, err)
			}
		})
	}
	// Count/length words are part of the aggregate budget, not just strings.
	var exact bytes.Buffer
	exact.Write(headerWords(4))
	for i := 0; i < 4; i++ {
		size := 16384
		if i == 3 {
			size -= 40
		}
		exact.Write(headerWords(1))
		exact.WriteByte(byte('a' + i))
		exact.Write(headerWords(uint32(size)))
		exact.Write(bytes.Repeat([]byte{'x'}, size))
	}
	if exact.Len() != 65536 {
		t.Fatal(exact.Len())
	}
	if _, err := parseHeaderValueBlock(bytes.NewReader(exact.Bytes()), 1); err != nil {
		t.Fatal(err)
	}
	raw := exact.Bytes()
	// Increase the last declared value by one; reject before its allocation.
	lastLength := 4 + 3*(9+16384) + 5
	binary.BigEndian.PutUint32(raw[lastLength:lastLength+4], 16384-39)
	if _, err := parseHeaderValueBlock(bytes.NewReader(raw), 1); err != errFrameBounds {
		t.Fatal(err)
	}
}
