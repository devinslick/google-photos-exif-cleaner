// exif-timestamp-fixer
//
// Scans a directory for media files missing EXIF timestamp data,
// then optionally stamps them with a default date/time.
// Optionally tags all files with an album/event name via XMP metadata.
//
// Requires exiftool (https://exiftool.org/).
//
// Usage:
//   exif-timestamp-fixer [directory] [flags]
//
// Flags:
//   -t, --timestamp     Timestamp to apply (e.g. "2022-01-19" or "2022-01-19 14:30:00")
//   -a, --album         Album/event name to tag all files with
//   -n, --dry-run       Preview without modifying any files
//   -r, --report-only   Only list missing timestamps; do not prompt to fix

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

const chunkSize = 50

var mediaExtensions = map[string]bool{
	".jpg": true, ".jpeg": true, ".heic": true, ".heif": true,
	".png": true, ".tiff": true, ".tif": true, ".bmp": true,
	".mp4": true, ".mov": true, ".avi": true, ".m4v": true,
	".3gp": true, ".mkv": true, ".webm": true,
	".gif": true, ".webp": true,
}

var timestampFields = []string{
	"DateTimeOriginal",
	"CreateDate",
	"MediaCreateDate",
	"TrackCreateDate",
	"ContentCreateDate",
	"GPSDateStamp",
}

// Common Windows install locations for exiftool, newest winget location first.
var exiftoolSearchPaths = []string{
	`AppData\Local\Programs\ExifTool\exiftool.exe`, // relative to USERPROFILE
	`C:\Program Files\ExifTool\exiftool.exe`,
	`C:\Program Files (x86)\ExifTool\exiftool.exe`,
	`C:\Windows\exiftool.exe`,
}

// ---------------------------------------------------------------------------
// Locate exiftool
// ---------------------------------------------------------------------------

func findExiftool() string {
	// Check PATH first
	if path, err := exec.LookPath("exiftool"); err == nil {
		return path
	}

	// Check known Windows locations
	home := os.Getenv("USERPROFILE")
	for _, rel := range exiftoolSearchPaths {
		var candidate string
		if filepath.IsAbs(rel) {
			candidate = rel
		} else {
			candidate = filepath.Join(home, rel)
		}
		if _, err := os.Stat(candidate); err == nil {
			if exec.Command(candidate, "-ver").Run() == nil {
				return candidate
			}
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// EXIF operations
// ---------------------------------------------------------------------------

type exifEntry map[string]interface{}

// batchGetTimestamps calls exiftool once per chunk and returns a map of
// normalised file path (forward slashes) -> exif data.
func batchGetTimestamps(exiftool string, files []string) map[string]exifEntry {
	result := make(map[string]exifEntry)

	fields := make([]string, len(timestampFields))
	for i, f := range timestampFields {
		fields[i] = "-" + f
	}

	for i := 0; i < len(files); i += chunkSize {
		end := i + chunkSize
		if end > len(files) {
			end = len(files)
		}
		chunk := files[i:end]

		args := append([]string{"-json"}, fields...)
		args = append(args, chunk...)

		out, err := exec.Command(exiftool, args...).Output()
		if err != nil {
			continue
		}

		var entries []exifEntry
		if err := json.Unmarshal(out, &entries); err != nil {
			continue
		}

		for _, entry := range entries {
			src, ok := entry["SourceFile"].(string)
			if !ok {
				continue
			}
			// exiftool always returns forward slashes
			result[strings.ReplaceAll(src, "\\", "/")] = entry
		}
	}
	return result
}

func hasValidTimestamp(entry exifEntry) bool {
	for _, field := range timestampFields {
		val, ok := entry[field]
		if !ok {
			continue
		}
		s, _ := val.(string)
		s = strings.TrimSpace(s)
		if s != "" && !strings.HasPrefix(s, "0000") && s != "0" {
			return true
		}
	}
	return false
}

// applyMetadata writes timestamp and/or album metadata to a file via exiftool.
// ts and album may each be empty; at least one must be non-empty.
func applyMetadata(exiftool, file, ts, album string, dryRun bool) bool {
	if dryRun {
		return true
	}
	var args []string
	if ts != "" {
		args = append(args,
			"-DateTimeOriginal="+ts,
			"-CreateDate="+ts,
			"-ModifyDate="+ts,
		)
	}
	if album != "" {
		args = append(args,
			"-XMP-iptcExt:Event="+album,
			"-XMP-dc:Subject+="+album,
			"-XMP-dc:Description="+album,    // Google Photos reads this for search
			"-IPTC:Caption-Abstract="+album, // broader compat
		)
	}
	args = append(args, "-overwrite_original", file)
	return exec.Command(exiftool, args...).Run() == nil
}

// normalisePath returns the absolute path with forward slashes (matching
// exiftool's SourceFile output).
func normalisePath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	return strings.ReplaceAll(abs, "\\", "/")
}

// ---------------------------------------------------------------------------
// Timestamp parsing
// ---------------------------------------------------------------------------

// Go's reference time: Mon Jan 2 15:04:05 MST 2006
var goFormats = []struct {
	layout  string
	hasTime bool
}{
	{"2006:01:02 15:04:05", true},
	{"2006-01-02 15:04:05", true},
	{"2006/01/02 15:04:05", true},
	{"2006:01:02", false},
	{"2006-01-02", false},
	{"2006/01/02", false},
	{"01/02/2006", false},
	{"01/02/2006 15:04:05", true},
}

func parseTimestamp(s string) (string, bool) {
	s = strings.TrimSpace(s)
	for _, f := range goFormats {
		t, err := time.Parse(f.layout, s)
		if err != nil {
			continue
		}
		if !f.hasTime {
			t = t.Add(12 * time.Hour) // default to noon when no time given
		}
		return t.Format("2006:01:02 15:04:05"), true
	}
	return "", false
}

// ---------------------------------------------------------------------------
// Filename timestamp extraction
// ---------------------------------------------------------------------------

// fileWithTs pairs a file path with a timestamp extracted from its filename.
type fileWithTs struct{ path, ts string }

// filenameTimestampRe matches compact format: YYYYMMDD[_-]HHMMSS[...]
// e.g. "20240709_182027.mp4", "PXL_20231123_182518628.TS.mp4"
var filenameTimestampRe = regexp.MustCompile(`(\d{4})(\d{2})(\d{2})[_-](\d{2})(\d{2})(\d{2})\d*`)

// filenameISODateRe matches ISO-dash format: YYYY-MM-DD optionally followed by [_-]HHMMSS[...]
// e.g. "2022-10-24-150226287.mp4", "2022-10-24.jpg"
var filenameISODateRe = regexp.MustCompile(`(\d{4})-(\d{2})-(\d{2})(?:[_-](\d{2})(\d{2})(\d{2})\d*)?`)

// filenameCompactNoSepRe matches exactly 14 consecutive digits as YYYYMMDDHHMMSS,
// bounded by non-digit characters (or start/end of string) to avoid matching
// within longer numeric IDs.
// e.g. "lv_7324034615860006160_20240617193045.mp4"
var filenameCompactNoSepRe = regexp.MustCompile(`(?:^|[^0-9])(\d{4})(\d{2})(\d{2})(\d{2})(\d{2})(\d{2})(?:[^0-9]|$)`)

// extractTimestampFromFilename looks for a date/time pattern in the filename
// and returns the timestamp in exiftool format, or "" if none found or invalid.
//
// Supported patterns:
//   - YYYYMMDD[_-]HHMMSS[...]  e.g. "20240709_182027.mp4", "PXL_20231123_182518628.TS.mp4"
//   - YYYY-MM-DD[-HHMMSS[...]] e.g. "2022-10-24-150226287.mp4", "2022-10-24.jpg"
//   - YYYYMMDDHHMMSS            e.g. "lv_7324034615860006160_20240617193045.mp4"
func extractTimestampFromFilename(name string) string {
	toInt := func(s string) int { n, _ := strconv.Atoi(s); return n }

	validate := func(yr, mo, dy, hr, mn, sc int) bool {
		return yr >= 1970 && yr <= 2099 && mo >= 1 && mo <= 12 && dy >= 1 && dy <= 31 &&
			hr <= 23 && mn <= 59 && sc <= 59
	}
	format := func(yr, mo, dy, hr, mn, sc int) string {
		return fmt.Sprintf("%04d:%02d:%02d %02d:%02d:%02d", yr, mo, dy, hr, mn, sc)
	}

	// Compact format: YYYYMMDD[_-]HHMMSS (time required)
	if m := filenameTimestampRe.FindStringSubmatch(name); m != nil {
		yr, mo, dy := toInt(m[1]), toInt(m[2]), toInt(m[3])
		hr, mn, sc := toInt(m[4]), toInt(m[5]), toInt(m[6])
		if validate(yr, mo, dy, hr, mn, sc) {
			return format(yr, mo, dy, hr, mn, sc)
		}
	}

	// ISO-dash format: YYYY-MM-DD, optionally followed by [_-]HHMMSS[...]
	if m := filenameISODateRe.FindStringSubmatch(name); m != nil {
		yr, mo, dy := toInt(m[1]), toInt(m[2]), toInt(m[3])
		if yr >= 1970 && yr <= 2099 && mo >= 1 && mo <= 12 && dy >= 1 && dy <= 31 {
			hr, mn, sc := 12, 0, 0 // default to noon when no time present
			if m[4] != "" {
				h, mi, s := toInt(m[4]), toInt(m[5]), toInt(m[6])
				if h <= 23 && mi <= 59 && s <= 59 {
					hr, mn, sc = h, mi, s
				}
			}
			return format(yr, mo, dy, hr, mn, sc)
		}
	}

	// No-separator compact format: exactly 14 digits as YYYYMMDDHHMMSS,
	// bounded by non-digits to avoid matching within longer numeric IDs.
	// e.g. "lv_7324034615860006160_20240617193045.mp4"
	if m := filenameCompactNoSepRe.FindStringSubmatch(name); m != nil {
		yr, mo, dy := toInt(m[1]), toInt(m[2]), toInt(m[3])
		hr, mn, sc := toInt(m[4]), toInt(m[5]), toInt(m[6])
		if validate(yr, mo, dy, hr, mn, sc) {
			return format(yr, mo, dy, hr, mn, sc)
		}
	}

	return ""
}

// ---------------------------------------------------------------------------
// Interactive prompts
// ---------------------------------------------------------------------------

var stdin = bufio.NewReader(os.Stdin)

func readLine(prompt string) string {
	fmt.Print(prompt)
	line, _ := stdin.ReadString('\n')
	return strings.TrimSpace(line)
}

func promptYesNo(question string, defaultYes bool) bool {
	hint := "[Y/n]"
	if !defaultYes {
		hint = "[y/N]"
	}
	answer := readLine(fmt.Sprintf("  %s %s: ", question, hint))
	if answer == "" {
		return defaultYes
	}
	lower := strings.ToLower(answer)
	return lower == "y" || lower == "yes"
}

func promptTimestamp() string {
	fmt.Println("\n  Enter a default timestamp to apply to files missing EXIF data.")
	fmt.Println("  Accepted: 2022-01-19  |  2022-01-19 14:30:00  |  2022:01:19 14:30:00")
	fmt.Println("  (Press Enter to cancel)")

	for {
		raw := readLine("\n  Timestamp: ")
		if raw == "" {
			return ""
		}
		if strings.ToLower(raw) == "quit" || strings.ToLower(raw) == "q" {
			os.Exit(0)
		}
		ts, ok := parseTimestamp(raw)
		if !ok {
			fmt.Println("  Could not parse that. Try: 2022-01-19 or 2022-01-19 14:30:00")
			continue
		}
		fmt.Printf("  Parsed as: %s\n", ts)
		if promptYesNo("Apply this timestamp?", true) {
			return ts
		}
	}
}

func promptAlbum() string {
	raw := readLine("\n  Album name (optional, press Enter to skip): ")
	if raw == "" {
		return ""
	}
	if promptYesNo(fmt.Sprintf("Tag all files with album %q?", raw), true) {
		return raw
	}
	return ""
}

// ---------------------------------------------------------------------------
// Simple flag parser (avoids flag package's exit-on-error behaviour and lets
// us accept both -flag and --flag styles)
// ---------------------------------------------------------------------------

type config struct {
	directory  string
	timestamp  string
	album      string
	dryRun     bool
	reportOnly bool
}

func parseArgs() config {
	cfg := config{}
	args := os.Args[1:]

	for i := 0; i < len(args); i++ {
		a := args[i]

		switch {
		case a == "--dry-run" || a == "-n":
			cfg.dryRun = true
		case a == "--report-only" || a == "-r":
			cfg.reportOnly = true
		case a == "--timestamp" || a == "-t":
			if i+1 < len(args) {
				i++
				cfg.timestamp = args[i]
			}
		case strings.HasPrefix(a, "--timestamp="):
			cfg.timestamp = strings.TrimPrefix(a, "--timestamp=")
		case strings.HasPrefix(a, "-t="):
			cfg.timestamp = strings.TrimPrefix(a, "-t=")
		case a == "--album" || a == "-a":
			if i+1 < len(args) {
				i++
				cfg.album = args[i]
			}
		case strings.HasPrefix(a, "--album="):
			cfg.album = strings.TrimPrefix(a, "--album=")
		case strings.HasPrefix(a, "-a="):
			cfg.album = strings.TrimPrefix(a, "-a=")
		case a == "--directory" || a == "-d":
			if i+1 < len(args) {
				i++
				cfg.directory = args[i]
			}
		case strings.HasPrefix(a, "--directory="):
			cfg.directory = strings.TrimPrefix(a, "--directory=")
		case strings.HasPrefix(a, "-d="):
			cfg.directory = strings.TrimPrefix(a, "-d=")
		case a == "--help" || a == "-h":
			printUsage()
			os.Exit(0)
		case !strings.HasPrefix(a, "-") && cfg.directory == "":
			// First bare argument is the directory
			cfg.directory = a
		}
	}
	return cfg
}

func printUsage() {
	fmt.Println(`EXIF Timestamp Fixer

Scans a directory for media files missing EXIF timestamps and
optionally stamps them with a default date/time.
Optionally tags all files with an album/event name via XMP metadata.

Usage:
  exif-timestamp-fixer [directory] [flags]

Flags:
  -t, --timestamp <datetime>   Timestamp to apply (e.g. "2022-01-19")
  -a, --album <name>           Album/event name to tag all files with
  -n, --dry-run                Preview without modifying files
  -r, --report-only            List missing timestamps; do not fix
  -h, --help                   Show this help

Examples:
  exif-timestamp-fixer "C:\Photos\NYC Trip"
  exif-timestamp-fixer "C:\Photos\NYC Trip" -t "2022-01-19"
  exif-timestamp-fixer "C:\Photos\NYC Trip" --album "NYC & New Jersey 2022"
  exif-timestamp-fixer "C:\Photos\NYC Trip" -t "2022-01-19" --album "NYC & New Jersey 2022"
  exif-timestamp-fixer "C:\Photos\NYC Trip" -t "2022-01-19 14:30:00" --dry-run
  exif-timestamp-fixer "C:\Photos\NYC Trip" --report-only`)
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

func main() {
	cfg := parseArgs()

	fmt.Println("==============================================================")
	fmt.Println("  EXIF Timestamp Fixer")
	fmt.Println("==============================================================")

	// Locate exiftool
	exiftool := findExiftool()
	if exiftool == "" {
		fmt.Fprintln(os.Stderr, "\n  ERROR: exiftool not found.")
		fmt.Fprintln(os.Stderr, "  Install from https://exiftool.org/")
		fmt.Fprintln(os.Stderr, "  Windows: winget install OliverBetz.ExifTool")
		os.Exit(1)
	}
	fmt.Println("  exiftool:", exiftool)

	// Resolve directory
	if cfg.directory == "" {
		cwd, _ := os.Getwd()
		fmt.Println("\n  Enter the path to the media folder to scan.")
		fmt.Printf("  (Press Enter for current directory: %s)\n", cwd)
		raw := readLine("  Directory: ")
		raw = strings.Trim(raw, `"'`)
		if raw == "" {
			cfg.directory = cwd
		} else {
			cfg.directory = raw
		}
	}

	absDir, err := filepath.Abs(cfg.directory)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n  ERROR: %v\n", err)
		os.Exit(1)
	}
	if info, err := os.Stat(absDir); err != nil || !info.IsDir() {
		fmt.Fprintf(os.Stderr, "\n  ERROR: Directory not found: %s\n", cfg.directory)
		os.Exit(1)
	}
	cfg.directory = absDir
	fmt.Println("  Directory:", cfg.directory)

	// Collect media files
	entries, err := os.ReadDir(cfg.directory)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n  ERROR reading directory: %v\n", err)
		os.Exit(1)
	}

	var mediaFiles []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if mediaExtensions[strings.ToLower(filepath.Ext(e.Name()))] {
			mediaFiles = append(mediaFiles, filepath.Join(cfg.directory, e.Name()))
		}
	}

	fmt.Printf("\n  Scanning %d media file(s) (batch mode)...\n", len(mediaFiles))

	exifMap := batchGetTimestamps(exiftool, mediaFiles)

	// Three-way categorisation:
	//   withExifTs   — has valid EXIF timestamp (no action needed)
	//   fromFilename — missing EXIF but filename encodes timestamp
	//   trulyMissing — no EXIF, no filename timestamp (needs manual date)
	var withExifTs []string
	var fromFilename []fileWithTs
	var trulyMissing []string

	for _, f := range mediaFiles {
		entry, ok := exifMap[normalisePath(f)]
		if ok && hasValidTimestamp(entry) {
			withExifTs = append(withExifTs, f)
		} else {
			ts := extractTimestampFromFilename(filepath.Base(f))
			if ts != "" {
				fromFilename = append(fromFilename, fileWithTs{f, ts})
			} else {
				trulyMissing = append(trulyMissing, f)
			}
		}
	}

	// Report
	fmt.Println("\n--------------------------------------------------------------")
	fmt.Printf("  Files with EXIF timestamps:     %d\n", len(withExifTs))
	fmt.Printf("  Files with filename timestamps: %d   ← will auto-apply\n", len(fromFilename))
	fmt.Printf("  Files missing all timestamps:   %d\n", len(trulyMissing))
	fmt.Println("--------------------------------------------------------------")

	if len(fromFilename) > 0 {
		fmt.Printf("\n  Files with filename timestamps (%d):\n", len(fromFilename))
		for _, fw := range fromFilename {
			fmt.Printf("    - %-40s  →  %s\n", filepath.Base(fw.path), fw.ts)
		}
	}

	if len(trulyMissing) > 0 {
		fmt.Printf("\n  Files missing all timestamps (%d):\n", len(trulyMissing))
		for _, f := range trulyMissing {
			fmt.Printf("    - %s\n", filepath.Base(f))
		}
	} else if len(fromFilename) == 0 {
		fmt.Println("\n  All media files already have timestamps.")
	}

	if cfg.reportOnly {
		if len(fromFilename) == 0 && len(trulyMissing) == 0 {
			fmt.Println("\n  Nothing to do.")
		} else {
			fmt.Println("\n  (report-only mode - no changes made)")
		}
		return
	}

	// === Filename-derived timestamps ===
	if len(fromFilename) > 0 {
		fmt.Println()
		if !promptYesNo(fmt.Sprintf("Apply filename-derived timestamps to %d file(s)?", len(fromFilename)), true) {
			// User declined — treat these as truly missing
			for _, fw := range fromFilename {
				trulyMissing = append(trulyMissing, fw.path)
			}
			fromFilename = nil
		}
	}

	// === Manual timestamp resolution for truly missing files ===
	willApplyTs := len(trulyMissing) > 0
	if willApplyTs {
		fmt.Println()
		if !promptYesNo(fmt.Sprintf("Apply a manual timestamp to %d file(s) missing all timestamps?", len(trulyMissing)), true) {
			willApplyTs = false
			trulyMissing = nil
		}
	}

	if willApplyTs {
		if cfg.timestamp == "" {
			cfg.timestamp = promptTimestamp()
			if cfg.timestamp == "" {
				willApplyTs = false
				trulyMissing = nil
			}
		} else {
			ts, ok := parseTimestamp(cfg.timestamp)
			if !ok {
				fmt.Fprintf(os.Stderr, "\n  ERROR: Could not parse timestamp: %s\n", cfg.timestamp)
				os.Exit(1)
			}
			cfg.timestamp = ts
			fmt.Println("  Timestamp:", cfg.timestamp)
		}
	}

	// === Album prompt ===
	if cfg.album == "" {
		cfg.album = promptAlbum()
	}

	// Nothing to do?
	if len(fromFilename) == 0 && !willApplyTs && cfg.album == "" {
		fmt.Println("\n  Nothing to do.")
		return
	}

	// Dry-run prompt (only if not already set via flag)
	if !cfg.dryRun {
		cfg.dryRun = promptYesNo("Dry run first (preview without modifying files)?", false)
	}

	if cfg.dryRun {
		fmt.Println("\n  DRY RUN - no files will be modified.")
	}

	// Print action summary
	if len(fromFilename) > 0 {
		fmt.Printf("\n  Applying filename-derived timestamps to %d file(s)...\n", len(fromFilename))
	}
	if willApplyTs {
		fmt.Printf("\n  Applying '%s' to %d file(s) missing all timestamps...\n", cfg.timestamp, len(trulyMissing))
	}
	if cfg.album != "" {
		fmt.Printf("\n  Tagging %d file(s) with album: %q\n", len(mediaFiles), cfg.album)
	}
	fmt.Println()

	// Build tsFor map: path -> timestamp to apply (empty = no timestamp change)
	tsFor := make(map[string]string)
	for _, fw := range fromFilename {
		tsFor[fw.path] = fw.ts
	}
	for _, f := range trulyMissing {
		tsFor[f] = cfg.timestamp
	}

	// Combined apply loop over all media files
	success, failed := 0, 0
	for _, f := range mediaFiles {
		ts := tsFor[f]
		if ts == "" && cfg.album == "" {
			continue
		}
		ok := applyMetadata(exiftool, f, ts, cfg.album, cfg.dryRun)
		status := "OK  "
		if !ok {
			status = "FAIL"
			failed++
		} else {
			success++
		}
		fmt.Printf("    [%s]  %s\n", status, filepath.Base(f))
	}

	fmt.Println("\n--------------------------------------------------------------")
	if cfg.dryRun {
		fmt.Printf("  DRY RUN complete - %d would be updated, %d would fail\n", success, failed)
	} else {
		fmt.Printf("  Done: %d updated, %d failed\n", success, failed)
	}
}
