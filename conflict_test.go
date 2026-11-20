// Copyright 2024 Bjørn Erik Pedersen
// SPDX-License-Identifier: MIT

package imagemeta_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bep/imagemeta"

	qt "github.com/frankban/quicktest"
)

const conflictDir = "testdata/conflict"

func openConflictFixture(t testing.TB, name string) *os.File {
	t.Helper()
	f, err := os.Open(filepath.Join(conflictDir, name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.Close(); err != nil {
			t.Errorf("close fixture %q: %v", name, err)
		}
	})
	return f
}

func decodeConflicts(t testing.TB, name string, opts ...func(*imagemeta.ConflictOptions)) (*imagemeta.ConflictView, error) {
	t.Helper()
	co := imagemeta.ConflictOptions{
		R:               openConflictFixture(t, name),
		ImageFormat:     imagemeta.JPEG,
		ShouldHandleTag: func(imagemeta.TagInfo) bool { return true },
	}
	for _, fn := range opts {
		fn(&co)
	}
	return imagemeta.DecodeConflicts(co)
}

func exifTags(v *imagemeta.ConflictView) map[string]imagemeta.TagInfo {
	t := v.Tags()
	return t.EXIF()
}

func xmpTags(v *imagemeta.ConflictView) map[string]imagemeta.TagInfo {
	t := v.Tags()
	return t.XMP()
}

func iptcTags(v *imagemeta.ConflictView) map[string]imagemeta.TagInfo {
	t := v.Tags()
	return t.IPTC()
}

func allTags(v *imagemeta.ConflictView) map[string]imagemeta.TagInfo {
	t := v.Tags()
	return t.All()
}

func diagKinds(view *imagemeta.ConflictView) map[string]imagemeta.Diagnostic {
	m := map[string]imagemeta.Diagnostic{}
	for _, d := range view.Diagnostics {
		m[d.Kind+"/"+d.Source.String()+"/"+d.Tag] = d
	}
	return m
}

// The fixed image carries deliberately different times, authors and
// copyright values in EXIF, IPTC and XMP.
func TestDecodeConflictsSourcesAndSelection(t *testing.T) {
	c := qt.New(t)

	view, err := decodeConflicts(t, "conflict.jpg")
	c.Assert(err, qt.IsNil)

	// Time: three different raw values, EXIF wins by default.
	dateTime, ok := view.Field(imagemeta.FieldDateTime)
	c.Assert(ok, qt.IsTrue)
	c.Assert(dateTime.HasConflict(), qt.IsTrue)
	c.Assert(dateTime.SelectedSource, qt.Equals, imagemeta.EXIF)
	c.Assert(dateTime.Selected, qt.Equals, "2020:01:02 11:00:00")
	c.Assert(dateTime.Values, qt.HasLen, 3)

	// Author: EXIF first value (the duplicate is dropped), then IPTC and XMP.
	creator, ok := view.Field(imagemeta.FieldCreator)
	c.Assert(ok, qt.IsTrue)
	c.Assert(creator.Selected, qt.Equals, "EXIF Author A")
	c.Assert(creator.SelectedSource, qt.Equals, imagemeta.EXIF)
	c.Assert(creator.Values, qt.HasLen, 3)
	c.Assert(creator.Values[1].Sources, qt.DeepEquals, []imagemeta.Source{imagemeta.IPTC})
	c.Assert(creator.Values[1].Tag, qt.Equals, "By-line")
	c.Assert(creator.Values[2].Value, qt.Equals, "XMP Author")

	// Copyright: IPTC and XMP carry the same value and must be merged,
	// while the differing EXIF value wins.
	copyright, ok := view.Field(imagemeta.FieldCopyright)
	c.Assert(ok, qt.IsTrue)
	c.Assert(copyright.Selected, qt.Equals, "EXIF Copyright")
	c.Assert(copyright.SelectedSource, qt.Equals, imagemeta.EXIF)
	c.Assert(copyright.Values, qt.HasLen, 2)
	c.Assert(copyright.Values[1].Value, qt.Equals, "Same Copyright")
	c.Assert(copyright.Values[1].Sources, qt.DeepEquals, []imagemeta.Source{imagemeta.IPTC, imagemeta.XMP})

	// Namespaces are preserved on every raw value.
	c.Assert(creator.Values[0].Namespace, qt.Equals, "IFD0")
	c.Assert(creator.Values[1].Namespace, qt.Equals, "IPTCApplication")
	c.Assert(creator.Values[2].Namespace, qt.Equals, "http://purl.org/dc/elements/1.1/")
}

// The priority order must be configurable and must drive the selected value.
func TestDecodeConflictsPriority(t *testing.T) {
	c := qt.New(t)

	view, err := decodeConflicts(t, "conflict.jpg", func(co *imagemeta.ConflictOptions) {
		co.Priority = []imagemeta.Source{imagemeta.XMP, imagemeta.IPTC, imagemeta.EXIF}
	})
	c.Assert(err, qt.IsNil)

	creator, ok := view.Field(imagemeta.FieldCreator)
	c.Assert(ok, qt.IsTrue)
	c.Assert(creator.Selected, qt.Equals, "XMP Author")
	c.Assert(creator.SelectedSource, qt.Equals, imagemeta.XMP)

	dateTime, ok := view.Field(imagemeta.FieldDateTime)
	c.Assert(ok, qt.IsTrue)
	c.Assert(dateTime.Selected, qt.Equals, "2022-04-04T14:00:00")
	c.Assert(dateTime.SelectedSource, qt.Equals, imagemeta.XMP)

	copyright, ok := view.Field(imagemeta.FieldCopyright)
	c.Assert(ok, qt.IsTrue)
	// XMP and IPTC agree; XMP is first in the priority list.
	c.Assert(copyright.Selected, qt.Equals, "Same Copyright")
	c.Assert(copyright.SelectedSource, qt.Equals, imagemeta.XMP)

	// Restricting the priority leaves the other sources out entirely.
	viewOnly, err := decodeConflicts(t, "conflict.jpg", func(co *imagemeta.ConflictOptions) {
		co.Priority = []imagemeta.Source{imagemeta.IPTC}
	})
	c.Assert(err, qt.IsNil)
	creator, ok = viewOnly.Field(imagemeta.FieldCreator)
	c.Assert(ok, qt.IsTrue)
	c.Assert(creator.Values, qt.HasLen, 1)
	c.Assert(creator.Values[0].Sources, qt.DeepEquals, []imagemeta.Source{imagemeta.IPTC})
	_, ok = exifTags(viewOnly)["Artist"]
	c.Assert(ok, qt.IsFalse)
}

func TestDecodeConflictsPriorityValidation(t *testing.T) {
	c := qt.New(t)

	_, err := imagemeta.DecodeConflicts(imagemeta.ConflictOptions{
		R:               strings.NewReader("foo"),
		ImageFormat:     imagemeta.JPEG,
		Priority:        []imagemeta.Source{imagemeta.EXIF, imagemeta.EXIF},
		ShouldHandleTag: func(imagemeta.TagInfo) bool { return true },
	})
	c.Assert(err, qt.ErrorMatches, "duplicate source .*")

	_, err = imagemeta.DecodeConflicts(imagemeta.ConflictOptions{
		R:               strings.NewReader("foo"),
		ImageFormat:     imagemeta.JPEG,
		Priority:        []imagemeta.Source{imagemeta.CONFIG},
		ShouldHandleTag: func(imagemeta.TagInfo) bool { return true },
	})
	c.Assert(err, qt.ErrorMatches, "unsupported source .*")
}

// Unknown tags stay available through Tags(), and every view value is JSON
// marshalable.
func TestDecodeConflictsUnknownTagsAndJSON(t *testing.T) {
	c := qt.New(t)

	view, err := decodeConflicts(t, "conflict.jpg")
	c.Assert(err, qt.IsNil)

	exif := exifTags(view)
	unknown, ok := exif["UnknownTag_0xc777"]
	c.Assert(ok, qt.IsTrue)
	c.Assert(unknown.Value, qt.Equals, "mystery")
	c.Assert(unknown.Namespace, qt.Equals, "IFD0")

	// The raw tags, including the unknown tag, must survive a JSON round trip.
	b, err := json.Marshal(allTags(view))
	c.Assert(err, qt.IsNil)
	c.Assert(string(b), qt.Contains, "UnknownTag_0xc777")
}

// Missing fields are absent rather than empty, and a source that carries no
// metadata simply contributes nothing.
func TestDecodeConflictsMissingFields(t *testing.T) {
	c := qt.New(t)

	// No EXIF segment at all: EXIF-backed fields fall through to the others,
	// EXIF-only fields (Orientation) are missing from the view.
	view, err := decodeConflicts(t, "conflict_missing_exif.jpg")
	c.Assert(err, qt.IsNil)

	creator, ok := view.Field(imagemeta.FieldCreator)
	c.Assert(ok, qt.IsTrue)
	// Default priority is EXIF, IPTC, XMP; with no EXIF the IPTC author wins.
	c.Assert(creator.SelectedSource, qt.Equals, imagemeta.IPTC)
	c.Assert(creator.Selected, qt.Equals, "IPTC Author")

	_, ok = view.Field(imagemeta.FieldOrientation)
	c.Assert(ok, qt.IsFalse)

	// No source errors for a segment that is legitimately absent.
	c.Assert(view.SourceErrors(), qt.HasLen, 0)
}

// A broken XMP block is a diagnostic, not a fatal error: EXIF and IPTC are
// still returned with their raw values.
func TestDecodeConflictsBrokenXMPBlock(t *testing.T) {
	c := qt.New(t)

	view, err := decodeConflicts(t, "conflict_broken_xmp.jpg")
	c.Assert(err, qt.IsNil)

	errs := view.SourceErrors()
	c.Assert(errs, qt.HasLen, 1)
	c.Assert(errs[0].Source, qt.Equals, imagemeta.XMP)
	c.Assert(errs[0].Message, qt.Contains, "decoding XMP")

	creator, ok := view.Field(imagemeta.FieldCreator)
	c.Assert(ok, qt.IsTrue)
	c.Assert(creator.Selected, qt.Equals, "EXIF Author A")
	c.Assert(creator.Values, qt.HasLen, 2) // EXIF and IPTC; no XMP entry
	for _, mv := range creator.Values {
		for _, s := range mv.Sources {
			c.Assert(s, qt.Not(qt.Equals), imagemeta.XMP)
		}
	}

	// The XMP tags map is empty, but EXIF and IPTC remain queryable.
	c.Assert(xmpTags(view), qt.HasLen, 0)
	c.Assert(iptcTags(view)["By-line"].Value, qt.Equals, "IPTC Author")
}

// A broken IPTC block is isolated to the IPTC source.
func TestDecodeConflictsBrokenIPTCBlock(t *testing.T) {
	c := qt.New(t)

	view, err := decodeConflicts(t, "conflict_broken_iptc.jpg")
	c.Assert(err, qt.IsNil)

	errs := view.SourceErrors()
	c.Assert(errs, qt.HasLen, 1)
	c.Assert(errs[0].Source, qt.Equals, imagemeta.IPTC)
	c.Assert(errs[0].Message, qt.Contains, "IPTC")

	headline, ok := view.Field(imagemeta.FieldHeadline)
	c.Assert(ok, qt.IsFalse)
	c.Assert(headline.Values, qt.HasLen, 0)

	creator, ok := view.Field(imagemeta.FieldCreator)
	c.Assert(ok, qt.IsTrue)
	c.Assert(creator.Values, qt.HasLen, 2) // EXIF and XMP only
}

// Oversized EXIF/IPTC values are skipped by the decoder and reported;
// oversized XMP values are reported but retained.
func TestDecodeConflictsOversizedValues(t *testing.T) {
	c := qt.New(t)

	view, err := decodeConflicts(t, "conflict.jpg")
	c.Assert(err, qt.IsNil)
	diags := diagKinds(view)

	exifOversized, ok := diags["oversized/EXIF/XPTitle"]
	c.Assert(ok, qt.IsTrue)
	c.Assert(exifOversized.Message, qt.Contains, "10050")
	c.Assert(exifOversized.Message, qt.Contains, "10000")
	c.Assert(exifOversized.Value, qt.IsNil)

	xmpOversized, ok := diags["oversized/XMP/Instructions"]
	c.Assert(ok, qt.IsTrue)
	c.Assert(xmpOversized.Value, qt.Not(qt.IsNil))
	instructions := xmpTags(view)["Instructions"]
	c.Assert(instructions.Value, qt.Equals, xmpOversized.Value)

	// A custom limit makes previously acceptable values oversized too.
	viewSmall, err := decodeConflicts(t, "conflict.jpg", func(co *imagemeta.ConflictOptions) {
		co.LimitTagSize = 5
		co.Priority = []imagemeta.Source{imagemeta.EXIF}
	})
	c.Assert(err, qt.IsNil)
	diagsSmall := diagKinds(viewSmall)
	_, ok = diagsSmall["oversized/EXIF/Artist"]
	c.Assert(ok, qt.IsTrue)
}

// A tag repeated within one source is diagnosed and de-duplicated; the first
// occurrence remains the selected value.
func TestDecodeConflictsDuplicateTags(t *testing.T) {
	c := qt.New(t)

	view, err := decodeConflicts(t, "conflict.jpg")
	c.Assert(err, qt.IsNil)
	diags := diagKinds(view)

	dup, ok := diags["duplicate/EXIF/Artist"]
	c.Assert(ok, qt.IsTrue)
	c.Assert(dup.Message, qt.Contains, "Artist")

	creator, ok := view.Field(imagemeta.FieldCreator)
	c.Assert(ok, qt.IsTrue)
	c.Assert(creator.Values[0].Value, qt.Equals, "EXIF Author A")
	// Only one EXIF entry despite two Artist tags in the fixture.
	c.Assert(creator.Values[0].Sources, qt.DeepEquals, []imagemeta.Source{imagemeta.EXIF})
}

// Formats without any metadata support and basic option errors must be
// reported with context rather than panicking.
func TestDecodeConflictsUnsupportedFormatAndOptions(t *testing.T) {
	c := qt.New(t)

	_, err := imagemeta.DecodeConflicts(imagemeta.ConflictOptions{
		R:               bytes.NewReader([]byte("GIF89a")),
		ImageFormat:     imagemeta.GIF,
		ShouldHandleTag: func(imagemeta.TagInfo) bool { return true },
	})
	c.Assert(err, qt.ErrorMatches, "unsupported image format: GIF")

	_, err = imagemeta.DecodeConflicts(imagemeta.ConflictOptions{})
	c.Assert(err, qt.ErrorMatches, "no reader provided")

	_, err = imagemeta.DecodeConflicts(imagemeta.ConflictOptions{
		R:               strings.NewReader("foo"),
		ImageFormat:     imagemeta.ImageFormat(1234),
		ShouldHandleTag: func(imagemeta.TagInfo) bool { return true },
	})
	c.Assert(err, qt.ErrorMatches, "unsupported image format.*")
}

// Ordinary Decode output must be unchanged: no new warnings on existing
// fixtures (the panicWarnf used by the shared helpers would fail the test),
// and the conflict view never writes to the reader's underlying bytes.
func TestDecodeConflictsLeavesOrdinaryDecodeUntouched(t *testing.T) {
	c := qt.New(t)

	before, err := os.ReadFile(filepath.Join(conflictDir, "conflict.jpg"))
	c.Assert(err, qt.IsNil)

	f, err := os.Open(filepath.Join(conflictDir, "conflict.jpg"))
	c.Assert(err, qt.IsNil)

	var ordinary imagemeta.Tags
	_, err = imagemeta.Decode(imagemeta.Options{
		R:               f,
		ImageFormat:     imagemeta.JPEG,
		Sources:         imagemeta.EXIF | imagemeta.IPTC | imagemeta.XMP,
		ShouldHandleTag: func(imagemeta.TagInfo) bool { return true },
		HandleTag:       func(ti imagemeta.TagInfo) error { ordinary.Add(ti); return nil },
		Warnf:           panicWarnf,
	})
	c.Assert(err, qt.IsNil)
	c.Assert(f.Close(), qt.IsNil)

	after, err := os.ReadFile(filepath.Join(conflictDir, "conflict.jpg"))
	c.Assert(err, qt.IsNil)
	c.Assert(bytes.Equal(before, after), qt.IsTrue)

	// The conflict view exposes the same raw tags the ordinary decoder does.
	view, err := decodeConflicts(t, "conflict.jpg")
	c.Assert(err, qt.IsNil)
	c.Assert(len(allTags(view)), qt.Equals, len(ordinary.All()))
	for source, tags := range map[imagemeta.Source]map[string]imagemeta.TagInfo{
		imagemeta.EXIF: exifTags(view),
		imagemeta.IPTC: iptcTags(view),
		imagemeta.XMP:  xmpTags(view),
	} {
		ordinaryTags := map[imagemeta.Source]map[string]imagemeta.TagInfo{
			imagemeta.EXIF: ordinary.EXIF(),
			imagemeta.IPTC: ordinary.IPTC(),
			imagemeta.XMP:  ordinary.XMP(),
		}[source]
		// Duplicate EXIF Artist: ordinary keeps the last value, the view the first.
		for tag, ti := range tags {
			if tag == "Artist" && source == imagemeta.EXIF {
				continue
			}
			other, found := ordinaryTags[tag]
			c.Assert(found, qt.IsTrue, qt.Commentf("%s tag %q missing from ordinary decode", source, tag))
			c.Assert(ti.Value, qt.DeepEquals, other.Value, qt.Commentf("%s tag %q", source, tag))
		}
	}
}

// The IPTC date and time pair is presented as a single DateTime value.
func TestDecodeConflictsIPTCDateTimePair(t *testing.T) {
	c := qt.New(t)

	view, err := decodeConflicts(t, "conflict_missing_exif.jpg", func(co *imagemeta.ConflictOptions) {
		co.Priority = []imagemeta.Source{imagemeta.IPTC}
	})
	c.Assert(err, qt.IsNil)

	dateTime, ok := view.Field(imagemeta.FieldDateTime)
	c.Assert(ok, qt.IsTrue)
	c.Assert(dateTime.Values, qt.HasLen, 1)
	c.Assert(dateTime.Selected, qt.Equals, "2021:03:03 12:30:45")
	c.Assert(dateTime.SelectedSource, qt.Equals, imagemeta.IPTC)
}

// The view also works against the ordinary, real-world test fixtures and
// reports consistent values where the sources agree. This guards the
// canonical field mappings against changes in the regular decode paths.
func TestDecodeConflictsOnRealFixture(t *testing.T) {
	c := qt.New(t)

	f, err := os.Open(filepath.Join("testdata", "images", "bep", "sunrise.jpg"))
	c.Assert(err, qt.IsNil)
	defer func() { c.Assert(f.Close(), qt.IsNil) }()

	view, err := imagemeta.DecodeConflicts(imagemeta.ConflictOptions{
		R:               f,
		ImageFormat:     imagemeta.JPEG,
		ShouldHandleTag: func(imagemeta.TagInfo) bool { return true },
	})
	c.Assert(err, qt.IsNil)
	c.Assert(view.SourceErrors(), qt.HasLen, 0)

	copyright, ok := view.Field(imagemeta.FieldCopyright)
	c.Assert(ok, qt.IsTrue)
	c.Assert(copyright.Selected, qt.Equals, "Bjørn Erik Pedersen")
	// Copyright agrees across sources and is merged into one value.
	c.Assert(copyright.Values, qt.HasLen, 1)
	c.Assert(copyright.Values[0].Sources, qt.DeepEquals, []imagemeta.Source{imagemeta.EXIF, imagemeta.IPTC, imagemeta.XMP})

	headline, ok := view.Field(imagemeta.FieldHeadline)
	c.Assert(ok, qt.IsTrue)
	c.Assert(headline.Selected, qt.Equals, "Sunrise in Spain")

	city, ok := view.Field(imagemeta.FieldCity)
	c.Assert(ok, qt.IsTrue)
	c.Assert(city.Selected, qt.Equals, "Benalmádena")
}
