// Copyright 2024 Bjørn Erik Pedersen
// SPDX-License-Identifier: MIT

package imagemeta

import (
	"fmt"
	"io"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"
)

// warnOversizedTag is the stable format used by the EXIF and IPTC decoders
// when a tag value exceeds Options.LimitTagSize and is skipped.
// Arguments: source label, tag name, actual size, configured limit.
const warnOversizedTag = "%s: skipping oversized tag %q of %d bytes; limit is %d bytes"

// oversizedWarningRe matches warnOversizedTag output and captures the tag name.
var oversizedWarningRe = regexp.MustCompile(`^(exif|iptc): skipping oversized tag "([^"]+)" of \d+ bytes; limit is \d+ bytes$`)

// Canonical field names exposed by the conflict view.
// A canonical field groups the equivalent tags from EXIF, IPTC and XMP.
const (
	// FieldTitle is a short reference name for the image
	// (EXIF XPTitle, IPTC ObjectName, XMP Title).
	FieldTitle = "Title"
	// FieldDescription is the long description/caption
	// (EXIF ImageDescription, IPTC Caption-Abstract, XMP Description).
	FieldDescription = "Description"
	// FieldHeadline is a brief publishable summary (IPTC Headline, XMP Headline).
	FieldHeadline = "Headline"
	// FieldCreator is the name of the person that created the image
	// (EXIF Artist, IPTC By-line, XMP Creator).
	FieldCreator = "Creator"
	// FieldCopyright is the copyright notice
	// (EXIF Copyright, IPTC CopyrightNotice, XMP Rights).
	FieldCopyright = "Copyright"
	// FieldDateTime is when the image was taken/created, combining
	// EXIF DateTimeOriginal/ModifyDate, IPTC DateCreated/TimeCreated and
	// XMP DateTimeOriginal/CreateDate/DateCreated.
	FieldDateTime = "DateTime"
	// FieldKeywords is the list of keywords/subjects
	// (EXIF XPKeywords, IPTC Keywords, XMP Subject).
	FieldKeywords = "Keywords"
	// FieldCity is the city where the image was taken
	// (EXIF City, IPTC City, XMP City).
	FieldCity = "City"
	// FieldCountry is the country where the image was taken
	// (EXIF Country, IPTC Country-PrimaryLocationName, XMP Country).
	FieldCountry = "Country"
	// FieldOrientation is the image orientation (EXIF Orientation only).
	FieldOrientation = "Orientation"
	// FieldGPSLatitude is the GPS latitude in decimal degrees.
	FieldGPSLatitude = "GPSLatitude"
	// FieldGPSLongitude is the GPS longitude in decimal degrees.
	FieldGPSLongitude = "GPSLongitude"
)

// canonicalFields maps a canonical field name to the tag names that carry it
// in each source. Tag names are the ones used in TagInfo.Tag.
var canonicalFields = map[string]map[Source][]string{
	FieldTitle: {
		EXIF: {"XPTitle"},
		IPTC: {"ObjectName"},
		XMP:  {"Title"},
	},
	FieldDescription: {
		EXIF: {"ImageDescription"},
		IPTC: {"Caption-Abstract"},
		XMP:  {"Description"},
	},
	FieldHeadline: {
		IPTC: {"Headline"},
		XMP:  {"Headline"},
	},
	FieldCreator: {
		EXIF: {"Artist"},
		IPTC: {"By-line"},
		XMP:  {"Creator"},
	},
	FieldCopyright: {
		EXIF: {"Copyright"},
		IPTC: {"CopyrightNotice"},
		XMP:  {"Rights"},
	},
	FieldDateTime: {
		EXIF: {"DateTimeOriginal", "ModifyDate"},
		IPTC: {"DateCreated", "TimeCreated"},
		XMP:  {"DateTimeOriginal", "CreateDate", "DateCreated"},
	},
	FieldKeywords: {
		EXIF: {"XPKeywords"},
		IPTC: {"Keywords"},
		XMP:  {"Subject"},
	},
	FieldCity: {
		EXIF: {"City"},
		IPTC: {"City"},
		XMP:  {"City"},
	},
	FieldCountry: {
		EXIF: {"Country"},
		IPTC: {"Country-PrimaryLocationName"},
		XMP:  {"Country"},
	},
	FieldOrientation: {
		EXIF: {"Orientation"},
	},
	FieldGPSLatitude: {
		EXIF: {"GPSLatitude"},
		XMP:  {"GPSLatitude"},
	},
	FieldGPSLongitude: {
		EXIF: {"GPSLongitude"},
		XMP:  {"GPSLongitude"},
	},
}

// Diagnostic kinds reported in a ConflictView.
const (
	// DiagnosticOversized is a tag whose value exceeded the configured size
	// limit. EXIF and IPTC tags are skipped by the decoder, so Value is nil;
	// XMP is not size limited by the decoder and the value is retained.
	DiagnosticOversized = "oversized"
	// DiagnosticDuplicate is a tag seen more than once within the same source.
	// The first occurrence is kept in the view.
	DiagnosticDuplicate = "duplicate"
	// DiagnosticSourceError is a metadata block that could not be decoded.
	// The other sources are still available in the view.
	DiagnosticSourceError = "source_error"
	// DiagnosticWarning is a non-fatal warning emitted while decoding a source.
	DiagnosticWarning = "warning"
)

// Diagnostic describes a non-fatal problem or a skipped piece of metadata
// observed while building a ConflictView.
type Diagnostic struct {
	// Kind is one of the Diagnostic* constants.
	Kind string
	// Source is the metadata source the diagnostic applies to.
	Source Source
	// Tag is the tag name when the diagnostic is tag specific, otherwise empty.
	Tag string
	// Namespace is the tag namespace when the diagnostic is tag specific.
	Namespace string
	// Message is a human readable description with error context.
	Message string
	// Value holds the value for oversized XMP tags; nil otherwise.
	Value any
}

// SourceValue is one raw value of a canonical field, as read from one source.
type SourceValue struct {
	// Source is the metadata source the value was read from.
	Source Source
	// Tag is the tag name inside the source that provided the value.
	Tag string
	// Namespace is the tag namespace (EXIF IFD path, IPTC record or XMP namespace URI).
	Namespace string
	// Value is the raw, unmodified decoded value.
	Value any
}

// FieldValue is the conflict view entry for one canonical field.
type FieldValue struct {
	// Field is the canonical field name (one of the Field* constants).
	Field string
	// Selected is the value chosen according to Options.Priority.
	// It is nil when every contributing source produced a nil value.
	Selected any
	// SelectedSource is the source Selected was taken from.
	// It is the zero Source when Selected is nil.
	SelectedSource Source
	// Values are the raw values from every source that carried the field,
	// in Options.Priority order. Equal values are merged into one entry,
	// but Sources lists every source that contributed that value.
	Values []MergedValue
}

// MergedValue is a distinct value of a canonical field together with the
// sources that carry it.
type MergedValue struct {
	// Value is the raw decoded value.
	Value any
	// Tag is the tag name that provided the value (from the first source).
	Tag string
	// Namespace is the namespace from the first source carrying this value.
	Namespace string
	// Sources is every source that carries this exact value, in priority order.
	Sources []Source
}

// ConflictOptions configures a DecodeConflicts call.
type ConflictOptions struct {
	// The Reader (typically a *os.File) to read image metadata from.
	R io.ReadSeeker

	// The image format in R.
	ImageFormat ImageFormat

	// Priority defines in which order sources win when a canonical field is
	// present in more than one source. It also defines which sources are read.
	// Defaults to [EXIF, IPTC, XMP].
	Priority []Source

	// If set, the decoder skips tags in which this function returns false.
	// If not set, the same default as for Decode is used.
	ShouldHandleTag func(tag TagInfo) bool

	// Warnf will be called for each warning, in addition to collecting the
	// warning as a Diagnostic. If nil, warnings are only collected.
	Warnf func(string, ...any)

	// DecompressBrotli, if set, is used to decompress Brotli compressed metadata.
	// See Options.DecompressBrotli.
	DecompressBrotli func(r io.Reader) io.Reader

	// Timeout is the maximum time the decoder will spend on reading metadata,
	// per source. If set to 0, the decoder will not time out.
	Timeout time.Duration

	// LimitNumTags is the maximum number of tags to read per source.
	// Default value is 5000.
	LimitNumTags uint32

	// LimitTagSize is the maximum size in bytes of an EXIF or IPTC tag value.
	// Larger values are skipped and reported as oversized diagnostics.
	// Default value is 10000.
	LimitTagSize uint32
}

// ConflictView is the result of a DecodeConflicts call.
// The image bytes are never modified; this is a read-only view.
type ConflictView struct {
	// Fields holds one entry per canonical field that was present in at least
	// one source, sorted by canonical field name.
	Fields []FieldValue
	// Diagnostics holds oversized, duplicate, warning and source error entries,
	// in the order they were observed.
	Diagnostics []Diagnostic

	// tags are the raw per-source tags, including unknown tags.
	tags Tags
	// limitTagSize is the effective per-tag size limit used when building the view.
	limitTagSize uint32
	// diagnosticsCount keeps track of duplicate diagnostics to avoid noise.
	diagnosticsSeen map[string]bool
}

// Tags returns every tag read from the image, grouped per source.
// This includes tags that are not part of the canonical field set and
// unknown tags (names prefixed with UnknownPrefix).
func (v *ConflictView) Tags() Tags {
	return v.tags
}

// Field returns the conflict view entry for a canonical field and whether it
// was present in any source. A missing field returns the zero FieldValue and
// false; that is not an error.
func (v *ConflictView) Field(name string) (FieldValue, bool) {
	for _, f := range v.Fields {
		if f.Field == name {
			return f, true
		}
	}
	return FieldValue{}, false
}

// HasConflict reports whether the field was present with different values in
// two or more sources.
func (f FieldValue) HasConflict() bool {
	return len(f.Values) > 1
}

// SourceErrors returns the diagnostics for metadata blocks that failed to
// decode. A broken EXIF, IPTC or XMP block therefore does not make
// DecodeConflicts fail as long as another source could be read.
func (v *ConflictView) SourceErrors() []Diagnostic {
	var diags []Diagnostic
	for _, d := range v.Diagnostics {
		if d.Kind == DiagnosticSourceError {
			diags = append(diags, d)
		}
	}
	return diags
}

func (v *ConflictView) addDiagnostic(d Diagnostic) {
	key := d.Kind + "|" + d.Source.String() + "|" + d.Tag + "|" + d.Message
	if v.diagnosticsSeen[key] {
		return
	}
	if v.diagnosticsSeen == nil {
		v.diagnosticsSeen = map[string]bool{}
	}
	v.diagnosticsSeen[key] = true
	v.Diagnostics = append(v.Diagnostics, d)
}

// DecodeConflicts reads the EXIF, IPTC and XMP metadata and builds a view of
// how the canonical fields are represented across the sources.
//
// Each source is decoded independently: a broken metadata block is reported as
// a Diagnostic and does not prevent the other sources from being read.
// Equal values across sources are merged, but every contributing source is
// retained, and the selected value follows opts.Priority.
//
// The ordinary Decode function and its results are unaffected by this call,
// and the image bytes are never modified.
func DecodeConflicts(opts ConflictOptions) (*ConflictView, error) {
	if opts.R == nil {
		return nil, fmt.Errorf("no reader provided")
	}

	priority := opts.Priority
	if len(priority) == 0 {
		priority = []Source{EXIF, IPTC, XMP}
	} else {
		seen := map[Source]bool{}
		for _, s := range priority {
			if s != EXIF && s != IPTC && s != XMP {
				return nil, fmt.Errorf("unsupported source %q in Priority; only EXIF, IPTC and XMP carry canonical fields", s)
			}
			if seen[s] {
				return nil, fmt.Errorf("duplicate source %q in Priority", s)
			}
			seen[s] = true
		}
	}

	// Validate the format up front so an unsupported format is a hard error
	// with context, mirroring Decode.
	if !formatSupportsMetadata(opts.ImageFormat) {
		return nil, fmt.Errorf("unsupported image format: %v", opts.ImageFormat)
	}

	view := &ConflictView{limitTagSize: opts.LimitTagSize}
	if view.limitTagSize == 0 {
		view.limitTagSize = defaultConflictTagSize
	}

	for _, source := range priority {
		// Each source gets a fresh decode from the start of the reader.
		if _, err := opts.R.Seek(0, io.SeekStart); err != nil {
			return nil, fmt.Errorf("rewind reader for %s: %w", source, err)
		}

		var warnings []string
		decodeOpts := Options{
			R:                   opts.R,
			ImageFormat:         opts.ImageFormat,
			Sources:             source,
			ShouldHandleTag:     opts.ShouldHandleTag,
			Warnf:               func(format string, args ...any) { warnings = append(warnings, fmt.Sprintf(format, args...)) },
			DecompressBrotli:    opts.DecompressBrotli,
			LimitNumTags:        opts.LimitNumTags,
			LimitTagSize:        opts.LimitTagSize,
			reportOversizedTags: true,
		}
		if opts.Timeout > 0 {
			decodeOpts.Timeout = opts.Timeout
		}

		seenInSource := map[string]bool{}
		decodeOpts.HandleTag = func(ti TagInfo) error {
			if seenInSource[ti.Tag] {
				view.addDiagnostic(Diagnostic{
					Kind:      DiagnosticDuplicate,
					Source:    ti.Source,
					Tag:       ti.Tag,
					Namespace: ti.Namespace,
					Message:   fmt.Sprintf("duplicate %s tag %q in namespace %q; keeping the first value", ti.Source, ti.Tag, ti.Namespace),
				})
				return nil
			}
			seenInSource[ti.Tag] = true
			view.tags.Add(ti)
			return nil
		}

		if _, err := Decode(decodeOpts); err != nil {
			view.addDiagnostic(Diagnostic{
				Kind:    DiagnosticSourceError,
				Source:  source,
				Message: fmt.Sprintf("%s metadata block could not be decoded: %v", source, err),
			})
		}

		for _, w := range warnings {
			if m := oversizedWarningRe.FindStringSubmatch(w); m != nil {
				view.addDiagnostic(Diagnostic{
					Kind:    DiagnosticOversized,
					Source:  source,
					Tag:     m[2],
					Message: w,
				})
			} else {
				view.addDiagnostic(Diagnostic{
					Kind:    DiagnosticWarning,
					Source:  source,
					Message: w,
				})
			}
			if opts.Warnf != nil {
				opts.Warnf("%s", w)
			}
		}
	}

	view.addOversizedDiagnostics()
	view.buildFields(priority)

	return view, nil
}

// addOversizedDiagnostics reports XMP values that exceed the size limit.
// The XMP decoder has no size limit, so the value is retained; EXIF and IPTC
// oversized tags are skipped by their decoders and reported from their
// warnings while DecodeConflicts collects them.
func (v *ConflictView) addOversizedDiagnostics() {
	xmp := v.tags.XMP()
	tags := make([]string, 0, len(xmp))
	for tag := range xmp {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	for _, tag := range tags {
		ti := xmp[tag]
		if size := valueByteSize(ti.Value); size > int(v.limitTagSize) {
			v.addDiagnostic(Diagnostic{
				Kind:      DiagnosticOversized,
				Source:    XMP,
				Tag:       tag,
				Namespace: ti.Namespace,
				Message:   fmt.Sprintf("xmp: tag %q value is %d bytes, exceeding the %d byte limit; value retained", tag, size, v.limitTagSize),
				Value:     ti.Value,
			})
		}
	}
}

const defaultConflictTagSize = 10000

// buildFields assembles the canonical field entries from the raw tags.
func (v *ConflictView) buildFields(priority []Source) {
	fieldNames := make([]string, 0, len(canonicalFields))
	for name := range canonicalFields {
		fieldNames = append(fieldNames, name)
	}
	sort.Strings(fieldNames)

	for _, field := range fieldNames {
		tagMap := canonicalFields[field]

		// Collect the raw source values in priority order. Multiple tags from
		// the same source may map to one canonical field (e.g. the IPTC date
		// and time pair); normalizeFieldValue collapses them into one entry
		// per source first.
		var raw []SourceValue
		for _, source := range priority {
			tags, ok := tagMap[source]
			if !ok {
				continue
			}
			sourceTags := v.sourceTags(source)
			for _, tag := range tags {
				if ti, found := sourceTags[tag]; found {
					raw = append(raw, SourceValue{
						Source:    source,
						Tag:       ti.Tag,
						Namespace: ti.Namespace,
						Value:     ti.Value,
					})
				}
			}
		}

		raw = v.normalizeFieldValue(field, raw)

		if len(raw) == 0 {
			// Missing field: simply absent from the view.
			continue
		}

		fv := FieldValue{Field: field}

		// Merge equal values while retaining every contributing source.
		for _, sv := range raw {
			merged := false
			for i := range fv.Values {
				if reflect.DeepEqual(fv.Values[i].Value, sv.Value) {
					fv.Values[i].Sources = append(fv.Values[i].Sources, sv.Source)
					merged = true
					break
				}
			}
			if !merged {
				fv.Values = append(fv.Values, MergedValue{
					Value:     sv.Value,
					Tag:       sv.Tag,
					Namespace: sv.Namespace,
					Sources:   []Source{sv.Source},
				})
			}
		}

		// Select the winning value following the priority order.
		for _, sv := range raw {
			if sv.Value != nil {
				fv.Selected = sv.Value
				fv.SelectedSource = sv.Source
				break
			}
		}

		v.Fields = append(v.Fields, fv)
	}
}

// normalizeFieldValue collapses the raw tags of one canonical field to at
// most one SourceValue per source. Most fields already satisfy that; DateTime
// is special because EXIF carries several date tags and IPTC splits the value
// into a date and a time record.
func (v *ConflictView) normalizeFieldValue(field string, raw []SourceValue) []SourceValue {
	if field != FieldDateTime {
		return raw
	}

	var out []SourceValue
	seen := map[Source]bool{}
	for _, sv := range raw {
		if seen[sv.Source] {
			continue
		}
		switch sv.Source {
		case IPTC:
			// Combine DateCreated and TimeCreated into a single value,
			// mirroring Tags.dateTime().
			iptc := v.tags.IPTC()
			dateTag, hasDate := iptc["DateCreated"]
			if !hasDate {
				continue
			}
			value := toString(dateTag.Value)
			if timeTag, hasTime := iptc["TimeCreated"]; hasTime {
				value += " " + toString(timeTag.Value)
			}
			out = append(out, SourceValue{
				Source:    IPTC,
				Tag:       "DateCreated",
				Namespace: dateTag.Namespace,
				Value:     value,
			})
			seen[IPTC] = true
		case EXIF:
			// DateTimeOriginal wins over ModifyDate within EXIF.
			if sv.Tag != "DateTimeOriginal" {
				if _, hasOriginal := v.tags.EXIF()["DateTimeOriginal"]; hasOriginal {
					continue
				}
			}
			out = append(out, sv)
			seen[EXIF] = true
		case XMP:
			out = append(out, sv)
			seen[XMP] = true
		}
	}
	return out
}

func (v *ConflictView) sourceTags(source Source) map[string]TagInfo {
	switch source {
	case EXIF:
		return v.tags.EXIF()
	case IPTC:
		return v.tags.IPTC()
	case XMP:
		return v.tags.XMP()
	default:
		return nil
	}
}

func valueByteSize(v any) int {
	switch vv := v.(type) {
	case string:
		return len(vv)
	case []byte:
		return len(vv)
	case []string:
		n := 0
		for _, s := range vv {
			n += len(s)
		}
		return n
	case nil:
		return 0
	default:
		return len(fmt.Sprintf("%v", vv))
	}
}

func formatSupportsMetadata(format ImageFormat) bool {
	switch format {
	case JPEG, TIFF, PNG, WebP, HEIF, AVIF, JXL, DNG, CR2, NEF, ARW, PEF:
		return true
	}
	return false
}

// String returns a compact, deterministic representation useful for logging.
func (f FieldValue) String() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s: selected=%v (%s)", f.Field, f.Selected, f.SelectedSource)
	for _, mv := range f.Values {
		fmt.Fprintf(&sb, "; %v=%v", mv.Sources, mv.Value)
	}
	return sb.String()
}
