/*
   Copyright 2014-2021 Docker Inc.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

// Copyright 2011 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package spdy

import (
	"compress/zlib"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"strings"
)

// Local receive limits: port-forward DATA is chunked, while stream metadata is
// small. Never use a peer count/length for allocation before checking these.
const (
	maxDataBytes             = 1 << 20
	maxCompressedHeaderBytes = 64 << 10
	maxHeaderBytes           = 64 << 10
	maxHeaders               = 256
	maxHeaderNameBytes       = 4 << 10
	maxHeaderValueBytes      = 16 << 10
	maxSettings              = 1024
)

var errFrameBounds = errors.New("SPDY frame exceeds receive bounds or has invalid framing")

func (frame *SynStreamFrame) read(h ControlFrameHeader, f *Framer) error {
	return f.readSynStreamFrame(h, frame)
}

func (frame *SynReplyFrame) read(h ControlFrameHeader, f *Framer) error {
	return f.readSynReplyFrame(h, frame)
}

func (frame *RstStreamFrame) read(h ControlFrameHeader, f *Framer) error {
	frame.CFHeader = h
	if err := binary.Read(f.r, binary.BigEndian, &frame.StreamId); err != nil {
		return err
	}
	if err := binary.Read(f.r, binary.BigEndian, &frame.Status); err != nil {
		return err
	}
	if frame.Status == 0 {
		return &Error{InvalidControlFrame, frame.StreamId}
	}
	if frame.StreamId == 0 {
		return &Error{ZeroStreamId, 0}
	}
	return nil
}

func (frame *SettingsFrame) read(h ControlFrameHeader, f *Framer) error {
	frame.CFHeader = h
	var numSettings uint32
	if err := binary.Read(f.r, binary.BigEndian, &numSettings); err != nil {
		return err
	}
	if numSettings > maxSettings || h.length < 4 || numSettings != (h.length-4)/8 || (h.length-4)%8 != 0 {
		return errFrameBounds
	}
	frame.FlagIdValues = make([]SettingsFlagIdValue, numSettings)
	for i := uint32(0); i < numSettings; i++ {
		if err := binary.Read(f.r, binary.BigEndian, &frame.FlagIdValues[i].Id); err != nil {
			return err
		}
		frame.FlagIdValues[i].Flag = SettingsFlag((frame.FlagIdValues[i].Id & 0xff000000) >> 24)
		frame.FlagIdValues[i].Id &= 0xffffff
		if err := binary.Read(f.r, binary.BigEndian, &frame.FlagIdValues[i].Value); err != nil {
			return err
		}
	}
	return nil
}

func (frame *PingFrame) read(h ControlFrameHeader, f *Framer) error {
	frame.CFHeader = h
	if err := binary.Read(f.r, binary.BigEndian, &frame.Id); err != nil {
		return err
	}
	if frame.Id == 0 {
		return &Error{ZeroStreamId, 0}
	}
	if frame.CFHeader.Flags != 0 {
		return &Error{InvalidControlFrame, StreamId(frame.Id)}
	}
	return nil
}

func (frame *GoAwayFrame) read(h ControlFrameHeader, f *Framer) error {
	frame.CFHeader = h
	if err := binary.Read(f.r, binary.BigEndian, &frame.LastGoodStreamId); err != nil {
		return err
	}
	if frame.CFHeader.Flags != 0 {
		return &Error{InvalidControlFrame, frame.LastGoodStreamId}
	}
	if frame.CFHeader.length != 8 {
		return &Error{InvalidControlFrame, frame.LastGoodStreamId}
	}
	if err := binary.Read(f.r, binary.BigEndian, &frame.Status); err != nil {
		return err
	}
	return nil
}

func (frame *HeadersFrame) read(h ControlFrameHeader, f *Framer) error {
	return f.readHeadersFrame(h, frame)
}

func (frame *WindowUpdateFrame) read(h ControlFrameHeader, f *Framer) error {
	frame.CFHeader = h
	if err := binary.Read(f.r, binary.BigEndian, &frame.StreamId); err != nil {
		return err
	}
	if frame.CFHeader.Flags != 0 {
		return &Error{InvalidControlFrame, frame.StreamId}
	}
	if frame.CFHeader.length != 8 {
		return &Error{InvalidControlFrame, frame.StreamId}
	}
	if err := binary.Read(f.r, binary.BigEndian, &frame.DeltaWindowSize); err != nil {
		return err
	}
	return nil
}

func newControlFrame(frameType ControlFrameType) (controlFrame, error) {
	ctor, ok := cframeCtor[frameType]
	if !ok {
		return nil, &Error{Err: InvalidControlFrame}
	}
	return ctor(), nil
}

var cframeCtor = map[ControlFrameType]func() controlFrame{
	TypeSynStream:    func() controlFrame { return new(SynStreamFrame) },
	TypeSynReply:     func() controlFrame { return new(SynReplyFrame) },
	TypeRstStream:    func() controlFrame { return new(RstStreamFrame) },
	TypeSettings:     func() controlFrame { return new(SettingsFrame) },
	TypePing:         func() controlFrame { return new(PingFrame) },
	TypeGoAway:       func() controlFrame { return new(GoAwayFrame) },
	TypeHeaders:      func() controlFrame { return new(HeadersFrame) },
	TypeWindowUpdate: func() controlFrame { return new(WindowUpdateFrame) },
}

func (f *Framer) uncorkHeaderDecompressor(payloadSize int64) error {
	if f.headerDecompressor != nil {
		f.headerReader.R = f.r
		f.headerReader.N = payloadSize
		return nil
	}
	f.headerReader = io.LimitedReader{R: f.r, N: payloadSize}
	decompressor, err := zlib.NewReaderDict(&f.headerReader, []byte(headerDictionary))
	if err != nil {
		return err
	}
	f.headerDecompressor = decompressor
	return nil
}

// ReadFrame reads SPDY encoded data and returns a decompressed Frame.
func (f *Framer) ReadFrame() (frame Frame, err error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	defer func() {
		if err != nil {
			f.readErr = err
		}
	}()
	var firstWord uint32
	if err := binary.Read(f.r, binary.BigEndian, &firstWord); err != nil {
		return nil, err
	}
	if firstWord&0x80000000 != 0 {
		frameType := ControlFrameType(firstWord & 0xffff)
		version := uint16(firstWord >> 16 & 0x7fff)
		return f.parseControlFrame(version, frameType)
	}
	return f.parseDataFrame(StreamId(firstWord & 0x7fffffff))
}

func (f *Framer) parseControlFrame(version uint16, frameType ControlFrameType) (Frame, error) {
	var length uint32
	if err := binary.Read(f.r, binary.BigEndian, &length); err != nil {
		return nil, err
	}
	flags := ControlFlags((length & 0xff000000) >> 24)
	length &= 0xffffff
	if version != Version {
		return nil, errFrameBounds
	}
	// Validate every supported envelope before reading a payload. Exact lengths
	// also prevent fixed-size decoders from consuming bytes of the next frame.
	var valid bool
	switch frameType {
	case TypeSynStream:
		valid = length >= 10 && length <= 10+maxCompressedHeaderBytes && flags & ^ControlFlags(3) == 0
	case TypeSynReply, TypeHeaders:
		valid = length >= 4 && length <= 4+maxCompressedHeaderBytes && flags & ^ControlFlags(1) == 0
	case TypeRstStream, TypeGoAway, TypeWindowUpdate:
		valid = length == 8 && flags == 0
	case TypePing:
		valid = length == 4 && flags == 0
	case TypeSettings:
		valid = length >= 4 && length <= 4+8*maxSettings && (length-4)%8 == 0 && flags & ^ControlFlags(1) == 0
	}
	if !valid {
		return nil, errFrameBounds
	}
	original := f.r
	payload := &io.LimitedReader{R: original, N: int64(length)}
	f.r = payload
	defer func() { f.r = original }()
	header := ControlFrameHeader{version, frameType, flags, length}
	cframe, err := newControlFrame(frameType)
	if err != nil {
		return nil, err
	}
	if err = cframe.read(header, f); err != nil {
		return nil, err
	}
	if payload.N != 0 {
		return nil, errFrameBounds
	}
	return cframe, nil
}

func parseHeaderValueBlock(r io.Reader, streamId StreamId) (http.Header, error) {
	// The budget covers counts, field length words, and field bytes together.
	// flate itself uses a fixed-size window; never drain untrusted extra output.
	budget := &io.LimitedReader{R: r, N: maxHeaderBytes}
	r = budget
	var numHeaders uint32
	if err := binary.Read(r, binary.BigEndian, &numHeaders); err != nil {
		return nil, err
	}
	if numHeaders > maxHeaders {
		return nil, errFrameBounds
	}
	var e error
	values := 0
	h := make(http.Header, int(numHeaders))
	for i := 0; i < int(numHeaders); i++ {
		var length uint32
		if err := binary.Read(r, binary.BigEndian, &length); err != nil {
			return nil, err
		}
		if length == 0 || length > maxHeaderNameBytes || int64(length) > budget.N {
			return nil, errFrameBounds
		}
		nameBytes := make([]byte, length)
		if _, err := io.ReadFull(r, nameBytes); err != nil {
			return nil, err
		}
		name := string(nameBytes)
		if name != strings.ToLower(name) {
			e = &Error{UnlowercasedHeaderName, streamId}
			name = strings.ToLower(name)
		}
		name = http.CanonicalHeaderKey(name)
		if h[name] != nil {
			e = &Error{DuplicateHeaders, streamId}
		}
		if err := binary.Read(r, binary.BigEndian, &length); err != nil {
			return nil, err
		}
		if length > maxHeaderValueBytes || int64(length) > budget.N {
			return nil, errFrameBounds
		}
		value := make([]byte, length)
		if _, err := io.ReadFull(r, value); err != nil {
			return nil, err
		}
		// Bound split slices too, and canonicalize the name once rather than
		// allocating a long canonical key separately for each NUL-separated value.
		values += 1 + strings.Count(string(value), headerValueSeparator)
		if values > maxHeaders {
			return nil, errFrameBounds
		}
		h[name] = append(h[name], strings.Split(string(value), headerValueSeparator)...)
	}
	if e != nil {
		return h, e
	}
	return h, nil
}

func (f *Framer) readSynStreamFrame(h ControlFrameHeader, frame *SynStreamFrame) error {
	frame.CFHeader = h
	var err error
	if err = binary.Read(f.r, binary.BigEndian, &frame.StreamId); err != nil {
		return err
	}
	if err = binary.Read(f.r, binary.BigEndian, &frame.AssociatedToStreamId); err != nil {
		return err
	}
	if err = binary.Read(f.r, binary.BigEndian, &frame.Priority); err != nil {
		return err
	}
	frame.Priority >>= 5
	if err = binary.Read(f.r, binary.BigEndian, &frame.Slot); err != nil {
		return err
	}
	reader := f.r
	if !f.headerCompressionDisabled {
		err := f.uncorkHeaderDecompressor(int64(h.length - 10))
		if err != nil {
			return err
		}
		reader = f.headerDecompressor
	}
	frame.Headers, err = parseHeaderValueBlock(reader, frame.StreamId)
	if !f.headerCompressionDisabled && (err == io.EOF && f.headerReader.N == 0 || f.headerReader.N != 0) {
		err = &Error{WrongCompressedPayloadSize, 0}
	}
	if err != nil {
		return err
	}
	for h := range frame.Headers {
		if invalidReqHeaders[h] {
			return &Error{InvalidHeaderPresent, frame.StreamId}
		}
	}
	if frame.StreamId == 0 {
		return &Error{ZeroStreamId, 0}
	}
	return nil
}

func (f *Framer) readSynReplyFrame(h ControlFrameHeader, frame *SynReplyFrame) error {
	frame.CFHeader = h
	var err error
	if err = binary.Read(f.r, binary.BigEndian, &frame.StreamId); err != nil {
		return err
	}
	reader := f.r
	if !f.headerCompressionDisabled {
		err := f.uncorkHeaderDecompressor(int64(h.length - 4))
		if err != nil {
			return err
		}
		reader = f.headerDecompressor
	}
	frame.Headers, err = parseHeaderValueBlock(reader, frame.StreamId)
	if !f.headerCompressionDisabled && (err == io.EOF && f.headerReader.N == 0 || f.headerReader.N != 0) {
		err = &Error{WrongCompressedPayloadSize, 0}
	}
	if err != nil {
		return err
	}
	for h := range frame.Headers {
		if invalidRespHeaders[h] {
			return &Error{InvalidHeaderPresent, frame.StreamId}
		}
	}
	if frame.StreamId == 0 {
		return &Error{ZeroStreamId, 0}
	}
	return nil
}

func (f *Framer) readHeadersFrame(h ControlFrameHeader, frame *HeadersFrame) error {
	frame.CFHeader = h
	var err error
	if err = binary.Read(f.r, binary.BigEndian, &frame.StreamId); err != nil {
		return err
	}
	reader := f.r
	if !f.headerCompressionDisabled {
		err := f.uncorkHeaderDecompressor(int64(h.length - 4))
		if err != nil {
			return err
		}
		reader = f.headerDecompressor
	}
	frame.Headers, err = parseHeaderValueBlock(reader, frame.StreamId)
	if !f.headerCompressionDisabled && (err == io.EOF && f.headerReader.N == 0 || f.headerReader.N != 0) {
		err = &Error{WrongCompressedPayloadSize, 0}
	}
	if err != nil {
		return err
	}
	var invalidHeaders map[string]bool
	if frame.StreamId%2 == 0 {
		invalidHeaders = invalidReqHeaders
	} else {
		invalidHeaders = invalidRespHeaders
	}
	for h := range frame.Headers {
		if invalidHeaders[h] {
			return &Error{InvalidHeaderPresent, frame.StreamId}
		}
	}
	if frame.StreamId == 0 {
		return &Error{ZeroStreamId, 0}
	}
	return nil
}

func (f *Framer) parseDataFrame(streamId StreamId) (*DataFrame, error) {
	var length uint32
	if err := binary.Read(f.r, binary.BigEndian, &length); err != nil {
		return nil, err
	}
	var frame DataFrame
	frame.StreamId = streamId
	frame.Flags = DataFlags(length >> 24)
	length &= 0xffffff
	if streamId == 0 || length > maxDataBytes || frame.Flags & ^DataFlags(1) != 0 {
		return nil, errFrameBounds
	}
	frame.Data = make([]byte, length)
	if _, err := io.ReadFull(f.r, frame.Data); err != nil {
		return nil, err
	}
	if frame.StreamId == 0 {
		return nil, &Error{ZeroStreamId, 0}
	}
	return &frame, nil
}
