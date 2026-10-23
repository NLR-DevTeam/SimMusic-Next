package audio

import (
	"encoding/binary"
	"errors"
	"io"

	"github.com/gopxl/beep/v2"
	gomp3 "github.com/hajimehoshi/go-mp3"
)

// An MP3 over the network is decoded as it arrives. go-mp3, given a source it can seek in, reads every frame
// of the file before it decodes the first (for the length and a table of where the frames start): over a
// network that is waiting for the whole download. Here go-mp3 gets a plain reader instead. The length comes
// from the Xing / Info header, or from the bitrate of a file without one; a seek maps the position to a byte
// by the header's table of contents (or in proportion), starts a new decoder there, and drops the first
// frame it decodes, which lacks the data of the frames before it.

const mp3BytesPerSample = 4 // go-mp3 always gives 16-bit stereo

type mp3Header struct {
	mpeg1               bool
	sampleRate, bitrate int // Hz, bits per second
	mono                bool
	size                int // bytes of the whole frame
}

var (
	mp3Bitrates1 = [16]int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0}
	mp3Bitrates2 = [16]int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0}
	mp3Rates1    = [4]int{44100, 48000, 32000, 0}
	mp3Rates2    = [4]int{22050, 24000, 16000, 0}
)

// parseMP3Header reads a layer III frame header of MPEG 1 or 2 (go-mp3 does not decode MPEG 2.5).
func parseMP3Header(b []byte) (mp3Header, bool) {
	if len(b) < 4 || b[0] != 0xff || b[1]&0xe0 != 0xe0 {
		return mp3Header{}, false
	}
	version, layer := b[1]>>3&3, b[1]>>1&3
	bitrateIndex, rateIndex, padding := b[2]>>4, b[2]>>2&3, int(b[2]>>1&1)
	if (version != 3 && version != 2) || layer != 1 || bitrateIndex == 0 || bitrateIndex == 15 || rateIndex == 3 {
		return mp3Header{}, false
	}
	h := mp3Header{mpeg1: version == 3, mono: b[3]>>6 == 3}
	if h.mpeg1 {
		h.bitrate, h.sampleRate = mp3Bitrates1[bitrateIndex]*1000, mp3Rates1[rateIndex]
		h.size = 144*h.bitrate/h.sampleRate + padding
	} else {
		h.bitrate, h.sampleRate = mp3Bitrates2[bitrateIndex]*1000, mp3Rates2[rateIndex]
		h.size = 72*h.bitrate/h.sampleRate + padding
	}
	return h, true
}

func (h mp3Header) samplesPerFrame() int {
	if h.mpeg1 {
		return 1152
	}
	return 576
}

// sideInfoSize is where the Xing / Info header begins after the four header bytes.
func (h mp3Header) sideInfoSize() int {
	switch {
	case h.mpeg1 && h.mono:
		return 17
	case h.mpeg1:
		return 32
	case h.mono:
		return 9
	}
	return 17
}

type mp3Stream struct {
	src        io.ReadSeeker
	audioStart int64 // the first frame of audio
	audioBytes int64 // from there to the end
	length     int   // samples
	toc        []byte
	dec        *gomp3.Decoder
	skip       int // decoded bytes still to drop after a seek
	pos        int
	buf        []byte
	err        error
}

// plainReader hides Seek from go-mp3, which would otherwise read the whole source first.
type plainReader struct{ r io.Reader }

func (p plainReader) Read(b []byte) (int, error) { return p.r.Read(b) }

// decodeMP3Stream opens an MP3 of total bytes (0 when unknown) for decoding as it arrives.
func decodeMP3Stream(src io.ReadSeeker, total int64) (beep.StreamSeekCloser, beep.Format, error) {
	if total <= 0 {
		return nil, beep.Format{}, errors.New("mp3: size unknown")
	}
	var tag [10]byte
	if _, err := io.ReadFull(src, tag[:]); err != nil {
		return nil, beep.Format{}, err
	}
	start := int64(0)
	if string(tag[:3]) == "ID3" {
		start = 10 + (int64(tag[6]&0x7f)<<21 | int64(tag[7]&0x7f)<<14 | int64(tag[8]&0x7f)<<7 | int64(tag[9]&0x7f))
		if tag[5]&0x10 != 0 {
			start += 10
		}
	}
	// The first frame: the header must be followed by another where its size says.
	if _, err := src.Seek(start, io.SeekStart); err != nil {
		return nil, beep.Format{}, err
	}
	window := make([]byte, 64<<10)
	n, _ := io.ReadFull(src, window)
	window = window[:n]
	first, at := mp3Header{}, -1
	for i := 0; i+4 <= len(window); i++ {
		h, ok := parseMP3Header(window[i:])
		if !ok {
			continue
		}
		if next, ok := parseMP3Header(window[min(len(window), i+h.size):]); ok && next.sampleRate == h.sampleRate || i+h.size+4 > len(window) {
			first, at = h, i
			break
		}
	}
	if at < 0 {
		return nil, beep.Format{}, errors.New("mp3: no frame found")
	}
	s := &mp3Stream{src: src, audioStart: start + int64(at)}
	s.audioBytes = total - s.audioStart
	frame := window[at:]
	xing := 4 + first.sideInfoSize()
	if len(frame) >= xing+8 && (string(frame[xing:xing+4]) == "Xing" || string(frame[xing:xing+4]) == "Info") {
		flags, p := binary.BigEndian.Uint32(frame[xing+4:]), xing+8
		if flags&1 != 0 && len(frame) >= p+4 {
			s.length = int(binary.BigEndian.Uint32(frame[p:])) * first.samplesPerFrame()
			p += 4
		}
		if flags&2 != 0 && len(frame) >= p+4 {
			if bytes := int64(binary.BigEndian.Uint32(frame[p:])); bytes > 0 && bytes <= s.audioBytes {
				s.audioBytes = bytes
			}
			p += 4
		}
		if flags&4 != 0 && len(frame) >= p+100 {
			s.toc = append([]byte(nil), frame[p:p+100]...)
		}
		// The header's own frame holds no audio.
		s.audioStart += int64(first.size)
		s.audioBytes -= int64(first.size)
	}
	if s.length <= 0 {
		s.length = int(float64(s.audioBytes) * 8 / float64(first.bitrate) * float64(first.sampleRate))
	}
	if err := s.start(s.audioStart, 0); err != nil {
		return nil, beep.Format{}, err
	}
	return s, beep.Format{SampleRate: beep.SampleRate(first.sampleRate), NumChannels: 2, Precision: 2}, nil
}

// start decodes from the byte offset on; drop frames' worth of output is discarded first.
func (s *mp3Stream) start(offset int64, drop int) error {
	if _, err := s.src.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	dec, err := gomp3.NewDecoder(plainReader{s.src})
	if err != nil {
		return err
	}
	s.dec, s.skip, s.buf = dec, drop, s.buf[:0]
	return nil
}

func (s *mp3Stream) Stream(samples [][2]float64) (int, bool) {
	if s.err != nil || s.dec == nil {
		return 0, false
	}
	need := len(samples) * mp3BytesPerSample
	for len(s.buf) < need {
		chunk := make([]byte, max(need-len(s.buf), 8192))
		n, err := s.dec.Read(chunk)
		chunk = chunk[:n]
		if s.skip > 0 {
			cut := min(s.skip, len(chunk))
			chunk, s.skip = chunk[cut:], s.skip-cut
		}
		s.buf = append(s.buf, chunk...)
		if err != nil {
			if err != io.EOF {
				s.err = err
			}
			break
		}
	}
	n := min(len(samples), len(s.buf)/mp3BytesPerSample)
	for i := 0; i < n; i++ {
		b := s.buf[i*mp3BytesPerSample:]
		samples[i][0] = float64(int16(binary.LittleEndian.Uint16(b))) / (1 << 15)
		samples[i][1] = float64(int16(binary.LittleEndian.Uint16(b[2:]))) / (1 << 15)
	}
	s.buf = s.buf[n*mp3BytesPerSample:]
	s.pos += n
	return n, n > 0
}

func (s *mp3Stream) Err() error { return s.err }

// Len is the length the headers tell; an estimate for a file of variable bitrate without them.
func (s *mp3Stream) Len() int { return max(s.length, s.pos) }

func (s *mp3Stream) Position() int { return s.pos }

func (s *mp3Stream) Seek(p int) error {
	p = max(0, min(p, s.length))
	share := float64(p) / float64(max(1, s.length))
	if len(s.toc) == 100 {
		percent := share * 100
		i := min(99, int(percent))
		low, high := float64(s.toc[i]), 256.0
		if i < 99 {
			high = float64(s.toc[i+1])
		}
		share = (low + (high-low)*(percent-float64(i))) / 256
	}
	offset := s.audioStart + int64(share*float64(s.audioBytes))
	// The frame before the one sought: decoded only for the data the next one takes from it.
	drop := 0
	if p > 0 {
		offset, drop = max(s.audioStart, offset-2048), 1152*mp3BytesPerSample
	}
	s.err = nil
	if err := s.start(offset, drop); err != nil {
		s.err = err
		return err
	}
	s.pos = p
	return nil
}

func (s *mp3Stream) Close() error { return nil }
