package archive

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sort"
	"time"
)

// Result describes the archive on disk after a successful WriteTarGz call.
// SHA256 is the lowercase hex digest of the .tar.gz bytes, computed in the
// same pass that wrote the file so callers do not have to re-read it.
type Result struct {
	Path   string
	Size   int64
	SHA256 string
}

// WriteTarGz writes a gzip-compressed tar of the named files. The keys of
// files are paths on disk; the values are the names to use inside the
// archive. Files are written in name-in-archive sort order for
// deterministic output. The returned Result carries the on-disk size and
// the SHA256 of the .tar.gz, both computed during this call.
func WriteTarGz(outPath string, files map[string]string) (Result, error) {
	// #nosec G304 -- outPath comes from the run orchestrator (defaults to
	// ./xray-export-<timestamp>.tar.gz; --out is the documented override).
	out, err := os.Create(outPath)
	if err != nil {
		return Result{}, fmt.Errorf("archive: create %s: %w", outPath, err)
	}
	defer out.Close()

	h := sha256.New()
	cw := &countingWriter{w: io.MultiWriter(out, h)}

	if err := writeArchive(cw, files); err != nil {
		return Result{}, err
	}

	return Result{
		Path:   outPath,
		Size:   cw.n,
		SHA256: hex.EncodeToString(h.Sum(nil)),
	}, nil
}

// archiveEpoch is the deterministic ModTime embedded in every file's tar
// header and the gzip stream header. Using a fixed value means two runs
// over identical data produce byte-identical archives (and so identical
// SHA256s) — the digest in the post-run summary is then a real artifact
// identity rather than a per-invocation token.
var archiveEpoch = time.Unix(0, 0).UTC()

func writeArchive(w io.Writer, files map[string]string) error {
	gz := gzip.NewWriter(w)
	gz.ModTime = archiveEpoch
	tw := tar.NewWriter(gz)

	type entry struct{ disk, name string }
	entries := make([]entry, 0, len(files))
	for disk, name := range files {
		entries = append(entries, entry{disk, name})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })

	for _, e := range entries {
		if err := writeFile(tw, e.disk, e.name); err != nil {
			_ = tw.Close()
			_ = gz.Close()
			return err
		}
	}
	if err := tw.Close(); err != nil {
		_ = gz.Close()
		return fmt.Errorf("archive: close tar: %w", err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("archive: close gzip: %w", err)
	}
	return nil
}

func writeFile(tw *tar.Writer, diskPath, archiveName string) error {
	// #nosec G304 -- diskPath is an internal path under the per-run temp dir.
	f, err := os.Open(diskPath)
	if err != nil {
		return fmt.Errorf("archive: open %s: %w", diskPath, err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("archive: stat %s: %w", diskPath, err)
	}

	hdr := &tar.Header{
		Name:    archiveName,
		Mode:    0o644,
		Size:    fi.Size(),
		ModTime: archiveEpoch,
		Format:  tar.FormatPAX,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("archive: write header %s: %w", archiveName, err)
	}
	if _, err := io.Copy(tw, f); err != nil {
		return fmt.Errorf("archive: copy %s: %w", archiveName, err)
	}
	return nil
}

// countingWriter tallies bytes written so the caller can report the
// on-disk archive size without a second stat.
type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

