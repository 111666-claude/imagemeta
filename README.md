[![Tests on Linux, MacOS and Windows](https://github.com/bep/imagemeta/workflows/Test/badge.svg)](https://github.com/bep/imagemeta/actions?query=workflow:Test)
[![codecov](https://codecov.io/gh/bep/imagemeta/branch/main/graph/badge.svg)](https://codecov.io/gh/bep/imagemeta)
[![GoDoc](https://godoc.org/github.com/bep/imagemeta?status.svg)](https://godoc.org/github.com/bep/imagemeta)

## This is about READING image metadata

Writing is not supported, and never will.

I welcome PRs with fixes, but please raise an issue first if you want to add new features.

## Supported formats

| Format                         | EXIF | IPTC | XMP | CONFIG |
| ------------------------------ | :--: | :--: | :-: | :----: |
| JPEG                           |  ✓   |  ✓   |  ✓  |   ✓    |
| TIFF                           |  ✓   |  ✓   |  ✓  |   ✓    |
| PNG                            |  ✓   |  ✓   |  ✓  |   ✓    |
| WebP                           |  ✓   |      |  ✓  |   ✓    |
| HEIF/HEIC                      |  ✓   |      |  ✓  |   ✓    |
| AVIF                           |  ✓   |      |  ✓  |   ✓    |
| JPEG XL                        |  ✓   |      |  ✓  |   ✓    |
| RAW (DNG, CR2, NEF, ARW, PEF)  |  ✓   |  ✓   |  ✓  |   ✓    |
| GIF                            |      |      |     |   ✓    |

CONFIG includes the frame count for GIF, animated PNG (APNG), WebP and AVIF/HEIF image sequences. JPEG XL animations are not detected and always report 1 frame.

JPEG XL files often store EXIF and XMP Brotli compressed (e.g. `cjxl` does this by default). We didn't want to force a Brotli decoder into everyone's build, so you need to provide one via the `DecompressBrotli` option to read these, e.g. using [andybalholm/brotli](https://github.com/andybalholm/brotli):

```go
imagemeta.Decode(imagemeta.Options{
	// ...
	DecompressBrotli: func(r io.Reader) io.Reader { return brotli.NewReader(r) },
})
```

If not set, compressed metadata is skipped with a warning.

Note that this library has no dependencies outside of the standard library and `golang.org/x/text`. The Brotli decoder is only imported by our tests, so it will not be compiled into your binary, but as with our other test dependencies, it will show up in e.g. `go mod graph`, `go list -m all` and your `go.sum`.

## Conflict view

The same field is commonly written to more than one metadata block — a photo's capture time can differ between EXIF and XMP. `DecodeConflicts` reads each supported source independently and returns, for a set of canonical fields, every raw value together with its source and namespace, plus the selected value.

```go
view, err := imagemeta.DecodeConflicts(imagemeta.ConflictOptions{
    R:           f,
    ImageFormat: imagemeta.JPEG,
    // Defaults to EXIF, IPTC, XMP; the order decides which value wins.
    Priority: []imagemeta.Source{imagemeta.EXIF, imagemeta.IPTC, imagemeta.XMP},
})
if err != nil {
    log.Fatal(err)
}

if created, found := view.Field(imagemeta.FieldDateTime); found {
    fmt.Println(created.Selected, created.SelectedSource)
    for _, v := range created.Values {
        fmt.Printf("  %v: %v (tag %q)\n", v.Sources, v.Value, v.Tag)
    }
}
```

Properties of the view:

* Equal values from different sources are merged into one entry, but every contributing source is retained, so it is always visible where a value came from.
* Unknown tags are not part of the canonical field set but remain available through `view.Tags()`.
* A broken EXIF, IPTC or XMP block is reported in `view.SourceErrors()` / `view.Diagnostics` and does not prevent the other sources from being read.
* Tags exceeding `LimitTagSize`, repeated tags and (for XMP, which has no decoder size limit) oversized values are reported as diagnostics with context. Missing fields are simply absent from the view, not errors.
* This is a read-only view: ordinary `Decode` behavior and results are unchanged, and image bytes are never modified.

## Performance

Extracting `EXIF` performs well, ref. the benhcmark below. Note that you can get a significant boost if you only need a subset of the fields (e.g. only the `Orientation`). The last line is with the library that [Hugo](https://github.com/gohugoio/hugo) used before it was replaced with this.

```bash
BenchmarkDecodeCompareWithGoexif/bep/imagemeta/exif/jpeg/alltags-10                68658             15825 ns/op            3034 B/op        128 allocs/op
BenchmarkDecodeCompareWithGoexif/bep/imagemeta/exif/jpeg/orientation-10           444249              2567 ns/op             501 B/op         11 allocs/op
BenchmarkDecodeCompareWithGoexif/rwcarlsen/goexif/exif/jpg/alltags-10              27206             44110 ns/op          141977 B/op        816 allocs/op
```

## When in doubt, Exiftool is right

The output of this library is tested against `exiftool -n -json`. This means, for example, that:

*  We use f-numbers and not APEX for aperture values.
*  We use seconds and not APEX for shutter speed values.
*  EXIF field definitions are fetched from this table:  https://exiftool.org/TagNames/EXIF.html
*  IPTC field definitions are fetched from this table:  https://exiftool.org/TagNames/IPTC.html
*  The XMP handling is currently very simple, you can supply your own XMP handler (see the `HandleXMP` option) if you need more.

There are some subtle differences in output:

* Exiftool prints rationale number arrays as space formatted strings with a format/precision that seems unnecessary hard to replicate, so we use `strconv.FormatFloat(f, 'f', -1, 64)` for these.

## Development

Many of the tests depends on generated golden files. To update these, run:

```bash
 go generate ./gen
```

Note that you need a working `exiftool` and `identify`(ImageMagick) in your `PATH` for this to work. `identify` will fail for the truncated RAW fixtures unless ImageMagick is built with libraw (Homebrew's is not); the generator then keeps the committed `.config.json` for those. This was tested OK with:

```
exiftool -ver
12.76
```

Debugging tips:

```bash
 exiftool testdata/goexif_samples/has-lens-info.jpg -htmldump > dump.html
 ```
