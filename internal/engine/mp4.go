package engine

import (
	"encoding/binary"
	"errors"
	"io"
)

// Just enough ISO-BMFF (fragmented MP4) handling to join a video-only and an
// audio-only stream into one playable file — what ffmpeg is normally used
// for — without any external tool: the two tracks get distinct IDs, their
// headers are combined into one "moov", and their fragments are interleaved.

type mp4Box struct {
	typ            string
	off, size, hdr int
}

func (b mp4Box) payload(data []byte) []byte { return data[b.off+b.hdr : b.off+b.size] }
func (b mp4Box) raw(data []byte) []byte     { return data[b.off : b.off+b.size] }

// mp4Boxes lists the boxes laid end to end in data.
func mp4Boxes(data []byte) []mp4Box {
	var out []mp4Box
	for off := 0; off+8 <= len(data); {
		size := int(binary.BigEndian.Uint32(data[off:]))
		hdr := 8
		switch size {
		case 1:
			if off+16 > len(data) {
				return out
			}
			size = int(binary.BigEndian.Uint64(data[off+8:]))
			hdr = 16
		case 0:
			size = len(data) - off
		}
		if size < hdr || off+size > len(data) {
			return out
		}
		out = append(out, mp4Box{string(data[off+4 : off+8]), off, size, hdr})
		off += size
	}
	return out
}

func mp4Find(data []byte, typ string) ([]byte, bool) {
	for _, b := range mp4Boxes(data) {
		if b.typ == typ {
			return b.payload(data), true
		}
	}
	return nil, false
}

func mp4Wrap(typ string, parts ...[]byte) []byte {
	n := 8
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 8, n)
	binary.BigEndian.PutUint32(out, uint32(n))
	copy(out[4:], typ)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// mp4SetTrackID rewrites the track ID inside a "trak" box (raw, with header).
func mp4SetTrackID(trak []byte, id uint32) {
	for _, b := range mp4Boxes(trak[8:]) {
		if b.typ != "tkhd" {
			continue
		}
		p := b.payload(trak[8:])
		off := 12 // version+flags, creation, modification (32-bit)
		if len(p) > 0 && p[0] == 1 {
			off = 20
		}
		if len(p) >= off+4 {
			binary.BigEndian.PutUint32(p[off:], id)
		}
	}
}

// mp4Init builds the header of the output file from the init segment of the
// video stream and, when the audio comes separately, of the audio stream.
// The video track becomes track 1 and the audio track 2. durSec, when known,
// is recorded so players can show the length and seek.
func mp4Init(video, audio []byte, durSec float64) ([]byte, error) {
	moov, ok := mp4Find(video, "moov")
	if !ok {
		return nil, errors.New("video init segment has no moov")
	}
	var ftyp, mvhd []byte
	for _, b := range mp4Boxes(video) {
		if b.typ == "ftyp" {
			ftyp = append([]byte(nil), b.raw(video)...)
		}
	}
	var traks, trexs, rest [][]byte
	collect := func(moov []byte, first bool) {
		for _, b := range mp4Boxes(moov) {
			raw := append([]byte(nil), b.raw(moov)...)
			switch b.typ {
			case "mvhd":
				if first {
					mvhd = raw
				}
			case "trak":
				traks = append(traks, raw)
			case "mvex":
				for _, c := range mp4Boxes(b.payload(moov)) {
					if c.typ == "trex" {
						trexs = append(trexs, append([]byte(nil), c.raw(b.payload(moov))...))
					}
				}
			default:
				if first {
					rest = append(rest, raw)
				}
			}
		}
	}
	collect(moov, true)
	if mvhd == nil || len(traks) == 0 {
		return nil, errors.New("video init segment is incomplete")
	}
	if audio != nil {
		amoov, ok := mp4Find(audio, "moov")
		if !ok {
			return nil, errors.New("audio init segment has no moov")
		}
		traks, trexs = traks[:1], trexs[:min(1, len(trexs))]
		nv := len(traks)
		collect(amoov, false)
		if len(traks) < nv+1 {
			return nil, errors.New("audio init segment has no track")
		}
		traks = traks[:nv+1]
		if len(trexs) > 2 {
			trexs = trexs[:2]
		}
		for i, t := range traks {
			mp4SetTrackID(t, uint32(i+1))
		}
		for i, t := range trexs {
			if len(t) >= 16 {
				binary.BigEndian.PutUint32(t[12:], uint32(i+1))
			}
		}
		binary.BigEndian.PutUint32(mvhd[len(mvhd)-4:], 3) // next_track_ID
	}

	mvex := trexs
	if durSec > 0 {
		p := mvhd[8:]
		tsOff := 12
		if p[0] == 1 {
			tsOff = 20
		}
		if len(p) >= tsOff+4 {
			timescale := binary.BigEndian.Uint32(p[tsOff:])
			mehd := make([]byte, 12)
			mehd[0] = 1 // version 1: 64-bit duration
			binary.BigEndian.PutUint64(mehd[4:], uint64(durSec*float64(timescale)))
			mvex = append([][]byte{mp4Wrap("mehd", mehd)}, mvex...)
		}
	}
	parts := [][]byte{mvhd}
	parts = append(parts, traks...)
	parts = append(parts, rest...)
	parts = append(parts, mp4Wrap("mvex", mvex...))
	return append(ftyp, mp4Wrap("moov", parts...)...), nil
}

// mp4Fragments copies the movie fragments (moof+mdat pairs) of one media
// segment to w. With track > 0 every fragment is reassigned to that track.
// seq numbers the fragments across the whole output; pos is the output
// offset, needed to fix up absolute data offsets.
func mp4Fragments(w io.Writer, seg []byte, track uint32, seq *uint32, pos *int64) error {
	for _, b := range mp4Boxes(seg) {
		switch b.typ {
		case "moof":
			moof := append([]byte(nil), b.raw(seg)...)
			for _, c := range mp4Boxes(moof[8:]) {
				p := c.payload(moof[8:])
				switch c.typ {
				case "mfhd":
					if len(p) >= 8 {
						*seq++
						binary.BigEndian.PutUint32(p[4:], *seq)
					}
				case "traf":
					for _, t := range mp4Boxes(p) {
						if t.typ != "tfhd" {
							continue
						}
						h := t.payload(p)
						if len(h) < 8 {
							continue
						}
						if track > 0 {
							binary.BigEndian.PutUint32(h[4:], track)
						}
						// An absolute base offset pointed into the source file; move it along.
						if h[3]&1 != 0 && len(h) >= 16 {
							base := int64(binary.BigEndian.Uint64(h[8:]))
							binary.BigEndian.PutUint64(h[8:], uint64(base-int64(b.off)+*pos))
						}
					}
				}
			}
			if _, err := w.Write(moof); err != nil {
				return err
			}
			*pos += int64(len(moof))
		case "mdat":
			if _, err := w.Write(b.raw(seg)); err != nil {
				return err
			}
			*pos += int64(b.size)
		}
	}
	return nil
}
