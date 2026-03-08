# google-photos-exif-cleaner

A command-line tool that scans a folder of media files for missing EXIF timestamps and fixes them — either by extracting the date from the filename or by applying a date you provide manually.

Designed for cleaning up photo/video libraries before or after importing into Google Photos, where missing timestamps cause files to be sorted incorrectly.

## Features

- Detects media files with no EXIF date metadata
- Auto-extracts timestamps from filenames in common formats:
  - `YYYYMMDD[_-]HHMMSS` — e.g. `20240709_182027.mp4`, `PXL_20231123_182518628.TS.mp4`
  - `YYYY-MM-DD[-HHMMSS]` — e.g. `2022-10-24-150226287.mp4`, `2022-10-24.jpg`
- Prompts for a manual fallback date for files with no recognizable filename pattern
- Optionally tags all files with an album/event name via XMP metadata (Google Photos reads this)
- Dry-run mode to preview changes before writing anything
- Report-only mode to audit a folder without modifying files
- Batch processing via exiftool for speed

## Requirements

- [exiftool](https://exiftool.org/) must be installed and accessible
  - **Windows:** `winget install OliverBetz.ExifTool`
  - **macOS:** `brew install exiftool`
  - **Linux:** `sudo apt install libimage-exiftool-perl`

## Installation

### Pre-built binary (Windows)

Download the latest `.exe` from the [Releases](https://github.com/devinslick/google-photos-exif-cleaner/releases) page.

### Build from source

Requires [Go](https://go.dev/) 1.21+.

```sh
git clone https://github.com/devinslick/google-photos-exif-cleaner.git
cd google-photos-exif-cleaner
go build -o google-photos-exif-cleaner .
```

## Usage

```
google-photos-exif-cleaner [directory] [flags]

Flags:
  -t, --timestamp <datetime>   Timestamp to apply (e.g. "2022-01-19")
  -a, --album <name>           Album/event name to tag all files with
  -n, --dry-run                Preview without modifying files
  -r, --report-only            List missing timestamps; do not fix
  -h, --help                   Show this help
```

Run without flags to use the interactive prompts:

```sh
google-photos-exif-cleaner "C:\Photos\NYC Trip"
```

Or pass arguments directly for scripted use:

```sh
google-photos-exif-cleaner "C:\Photos\NYC Trip" -t "2022-01-19" --album "NYC Trip Jan 2022"
google-photos-exif-cleaner "C:\Photos\NYC Trip" -t "2022-01-19 14:30:00" --dry-run
google-photos-exif-cleaner "C:\Photos\NYC Trip" --report-only
```

## How it works

1. Scans the target directory for media files (`.jpg`, `.heic`, `.mp4`, `.mov`, etc.)
2. Calls exiftool in batch mode to read existing EXIF timestamps
3. Categorizes each file:
   - **Has EXIF timestamp** — skipped, no action needed
   - **No EXIF, but filename contains a date** — timestamp is extracted automatically
   - **No EXIF, no recognizable date in filename** — queued for manual timestamp
4. Prompts you to confirm before writing anything (unless flags were passed)
5. Writes timestamp and/or album metadata via exiftool using `-overwrite_original`

## Supported media types

Images: `.jpg`, `.jpeg`, `.heic`, `.heif`, `.png`, `.tiff`, `.tif`, `.bmp`, `.gif`, `.webp`

Video: `.mp4`, `.mov`, `.avi`, `.m4v`, `.3gp`, `.mkv`, `.webm`

## License

MIT
