// Command conflictfixture generates fixed JPEG images with known, conflicting
// EXIF, IPTC and XMP metadata for the DecodeConflicts tests.
//
// Run from the repository root:
//
//	go run ./gen/conflictfixture
//
// The generated files are committed to testdata/conflict and must not change
// between runs (the generator is deterministic). The images contain no pixel
// data beyond SOI/EOI; only metadata is exercised.
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
)

const outDir = "testdata/conflict"

func main() {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fail(err)
	}

	writeFile("conflict.jpg", buildJPEG(buildEXIF(), buildIPTC(), buildXMP(10050)))
	writeFile("conflict_missing_exif.jpg", buildJPEG(nil, buildIPTC(), buildXMP(10050)))
	writeFile("conflict_broken_xmp.jpg", buildJPEG(buildEXIF(), buildIPTC(), buildBrokenXMP()))
	writeFile("conflict_broken_iptc.jpg", buildJPEGBrokenIPTC(buildEXIF(), buildXMP(10050)))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func writeFile(name string, b []byte) {
	if err := os.WriteFile(filepath.Join(outDir, name), b, 0o644); err != nil {
		fail(err)
	}
	fmt.Printf("wrote %s (%d bytes)\n", filepath.Join(outDir, name), len(b))
}

// must fails the generator on a write error. The in-memory buffers only fail
// on out-of-memory, so errors are not expected in practice.
func must(err error) {
	if err != nil {
		fail(err)
	}
}

// buildJPEG assembles a JPEG with the given metadata segments. Any argument
// may be nil to omit that source.
func buildJPEG(exif, iptc, xmp []byte) []byte {
	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xD8}) // SOI
	if exif != nil {
		b.Write(segment(0xFFE1, append([]byte("Exif\x00\x00"), exif...)))
	}
	if iptc != nil {
		b.Write(segment(0xFFED, iptc))
	}
	if xmp != nil {
		b.Write(segment(0xFFE1, append([]byte("http://ns.adobe.com/xap/1.0/\x00"), xmp...)))
	}
	b.Write([]byte{0xFF, 0xD9}) // EOI
	return b.Bytes()
}

// segment wraps payload in a JPEG segment: marker + 2-byte length + payload.
func segment(marker uint16, payload []byte) []byte {
	var b bytes.Buffer
	must(binary.Write(&b, binary.BigEndian, marker))
	must(binary.Write(&b, binary.BigEndian, uint16(len(payload)+2)))
	b.Write(payload)
	return b.Bytes()
}

// --- EXIF -----------------------------------------------------------------

const (
	exifTypeASCII = 2
	exifTypeShort = 3
	exifTypeLong  = 4
	exifTypeByte  = 7
)

type exifEntry struct {
	tagID uint16
	typ   uint16
	value []byte // raw value bytes; count is derived from the type
}

// buildEXIF creates a little-endian TIFF block with conflicting, duplicate,
// unknown and oversized tags in IFD0 and a DateTimeOriginal in the Exif IFD.
func buildEXIF() []byte {
	entries := []exifEntry{
		{0x010E, exifTypeASCII, appendZ("EXIF caption")},         // ImageDescription
		{0x013B, exifTypeASCII, appendZ("EXIF Author A")},        // Artist (first)
		{0x013B, exifTypeASCII, appendZ("EXIF Author B")},        // Artist (duplicate)
		{0x8298, exifTypeASCII, appendZ("EXIF Copyright")},       // Copyright
		{0x0132, exifTypeASCII, appendZ("2020:01:01 10:00:00")},  // ModifyDate
		{0x9C9B, exifTypeByte, bytes.Repeat([]byte("X"), 10050)}, // XPTitle, oversized
		{0x8769, exifTypeLong, make([]byte, 4)},                  // ExifIFD pointer, filled later
		{0xC777, exifTypeASCII, appendZ("mystery")},              // Unknown tag
	}
	exifIFDEntries := []exifEntry{
		{0x9003, exifTypeASCII, appendZ("2020:01:02 11:00:00")}, // DateTimeOriginal
	}

	const tiffHeaderLen = 8
	ifd0Offset := uint32(tiffHeaderLen)
	ifd0Len := uint32(2 + len(entries)*12 + 4)

	// Data area for IFD0 values longer than 4 bytes.
	var ifd0Data bytes.Buffer
	ifd0DataOffsets := make(map[int]uint32)
	for i, e := range entries {
		if e.tagID == 0x8769 || len(e.value) <= 4 {
			continue
		}
		ifd0DataOffsets[i] = ifd0Offset + ifd0Len + uint32(ifd0Data.Len())
		ifd0Data.Write(e.value)
		if len(e.value)%2 != 0 {
			ifd0Data.WriteByte(0)
		}
	}

	exifIFDOffset := ifd0Offset + ifd0Len + uint32(ifd0Data.Len())
	exifIFDLen := uint32(2 + len(exifIFDEntries)*12 + 4)

	var exifIFDData bytes.Buffer
	exifIFDDataOffsets := make(map[int]uint32)
	for i, e := range exifIFDEntries {
		if len(e.value) <= 4 {
			continue
		}
		exifIFDDataOffsets[i] = exifIFDOffset + exifIFDLen + uint32(exifIFDData.Len())
		exifIFDData.Write(e.value)
		if len(e.value)%2 != 0 {
			exifIFDData.WriteByte(0)
		}
	}

	var b bytes.Buffer
	b.WriteString("II")
	must(binary.Write(&b, binary.LittleEndian, uint16(42)))
	must(binary.Write(&b, binary.LittleEndian, ifd0Offset))

	must(binary.Write(&b, binary.LittleEndian, uint16(len(entries))))
	for i, e := range entries {
		must(binary.Write(&b, binary.LittleEndian, e.tagID))
		must(binary.Write(&b, binary.LittleEndian, e.typ))
		must(binary.Write(&b, binary.LittleEndian, exifCount(e)))
		switch {
		case e.tagID == 0x8769:
			must(binary.Write(&b, binary.LittleEndian, exifIFDOffset))
		case len(e.value) <= 4:
			b.Write(pad4(e.value))
		default:
			must(binary.Write(&b, binary.LittleEndian, ifd0DataOffsets[i]))
		}
	}
	must(binary.Write(&b, binary.LittleEndian, uint32(0))) // no IFD1
	b.Write(ifd0Data.Bytes())

	must(binary.Write(&b, binary.LittleEndian, uint16(len(exifIFDEntries))))
	for i, e := range exifIFDEntries {
		must(binary.Write(&b, binary.LittleEndian, e.tagID))
		must(binary.Write(&b, binary.LittleEndian, e.typ))
		must(binary.Write(&b, binary.LittleEndian, exifCount(e)))
		if len(e.value) <= 4 {
			b.Write(pad4(e.value))
		} else {
			must(binary.Write(&b, binary.LittleEndian, exifIFDDataOffsets[i]))
		}
	}
	must(binary.Write(&b, binary.LittleEndian, uint32(0)))
	b.Write(exifIFDData.Bytes())

	return b.Bytes()
}

func exifCount(e exifEntry) uint32 {
	switch e.typ {
	case exifTypeShort:
		return uint32(len(e.value) / 2)
	case exifTypeLong:
		return uint32(len(e.value) / 4)
	default:
		return uint32(len(e.value))
	}
}

func appendZ(s string) []byte { return append([]byte(s), 0) }

func pad4(b []byte) []byte {
	for len(b) < 4 {
		b = append(b, 0)
	}
	return b
}

// --- IPTC -----------------------------------------------------------------

// buildIPTC creates an APP13 Photoshop-style block with IPTC IIM records.
func buildIPTC() []byte {
	var records bytes.Buffer
	iptcRecord := func(record, dataset uint8, value string) {
		records.WriteByte(0x1C)
		records.WriteByte(record)
		records.WriteByte(dataset)
		must(binary.Write(&records, binary.BigEndian, uint16(len(value))))
		records.WriteString(value)
	}
	iptcRecord(1, 90, "\x1b%G")          // CodedCharacterSet: UTF-8
	iptcRecord(2, 80, "IPTC Author")     // By-line
	iptcRecord(2, 116, "Same Copyright") // CopyrightNotice (equals XMP)
	iptcRecord(2, 55, "20210303")        // DateCreated
	iptcRecord(2, 60, "123045")          // TimeCreated
	iptcRecord(2, 105, "IPTC Headline")  // Headline
	iptcRecord(2, 120, "IPTC caption")   // Caption-Abstract

	var block bytes.Buffer
	block.WriteString("Photoshop 3.0\x00")
	block.WriteString("8BIM")
	must(binary.Write(&block, binary.BigEndian, uint16(0x0404)))
	block.Write([]byte{0x00, 0x00}) // empty Pascal name + padding
	must(binary.Write(&block, binary.BigEndian, uint32(records.Len())))
	block.Write(records.Bytes())
	return block.Bytes()
}

// brokenIPTCPayload returns the payload of an APP13 block whose inner 8BIM
// record declares more data than the payload contains.
func brokenIPTCPayload() []byte {
	var block bytes.Buffer
	block.WriteString("Photoshop 3.0\x00")
	block.WriteString("8BIM")
	must(binary.Write(&block, binary.BigEndian, uint16(0x0404)))
	block.Write([]byte{0x00, 0x00})
	must(binary.Write(&block, binary.BigEndian, uint32(100000)))
	block.Write([]byte{0x1C, 0x02, 0x50, 0x00}) // truncated By-line header
	return block.Bytes()
}

// buildJPEGBrokenIPTC writes the IPTC APP13 segment with an outer length that
// exceeds the actual payload, so the JPEG decoder fails with an unexpected EOF
// while the EXIF and XMP segments remain intact. The broken segment is last so
// its overstated length cannot disturb the independent source decodes.
func buildJPEGBrokenIPTC(exif, xmp []byte) []byte {
	payload := brokenIPTCPayload()
	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xD8})
	if exif != nil {
		b.Write(segment(0xFFE1, append([]byte("Exif\x00\x00"), exif...)))
	}
	if xmp != nil {
		b.Write(segment(0xFFE1, append([]byte("http://ns.adobe.com/xap/1.0/\x00"), xmp...)))
	}
	b.Write([]byte{0xFF, 0xED})
	must(binary.Write(&b, binary.BigEndian, uint16(len(payload)+2+500)))
	b.Write(payload)
	b.Write([]byte{0xFF, 0xD9})
	return b.Bytes()
}

// --- XMP ------------------------------------------------------------------

func buildXMP(instructionsLen int) []byte {
	instructions := bytes.Repeat([]byte("Z"), instructionsLen)
	return []byte(fmt.Sprintf(`<x:xmpmeta xmlns:x="adobe:ns:meta/">
<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">
<rdf:Description rdf:about=""
 xmlns:dc="http://purl.org/dc/elements/1.1/"
 xmlns:xmp="http://ns.adobe.com/xap/1.0/"
 xmlns:photoshop="http://ns.adobe.com/photoshop/1.0/"
 dc:creator="XMP Author"
 dc:rights="Same Copyright"
 xmp:CreateDate="2022-04-04T14:00:00"
 dc:title="XMP Title"
 dc:description="XMP caption">
<dc:subject><rdf:Bag><rdf:li>news</rdf:li><rdf:li>politics</rdf:li></rdf:Bag></dc:subject>
<photoshop:Instructions>%s</photoshop:Instructions>
</rdf:Description>
</rdf:RDF>
</x:xmpmeta>
`, instructions))
}

// buildBrokenXMP is not well-formed XML; the XMP decoder must report it
// without taking the other sources down.
func buildBrokenXMP() []byte {
	return []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF><rdf:Description dc:creator="XMP Author"></unclosed-tag>`)
}
