package archive

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"sort"
	"time"
)

// WriteTarGz writes a gzip-compressed tar of the named files. The keys of
// files are paths on disk; the values are the names to use inside the
// archive. Files are written in name-in-archive sort order for
// deterministic output.
func WriteTarGz(outPath string, files map[string]string) error {
	// #nosec G304 -- outPath comes from the run orchestrator (defaults to
	// ./xray-export-<timestamp>.tar.gz; --out is the documented override).
	out, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("archive: create %s: %w", outPath, err)
	}
	defer out.Close()

	gz := gzip.NewWriter(out)
	defer gz.Close()

	tw := tar.NewWriter(gz)
	defer tw.Close()

	// Sort by archive name for deterministic ordering.
	type entry struct{ disk, name string }
	entries := make([]entry, 0, len(files))
	for disk, name := range files {
		entries = append(entries, entry{disk, name})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })

	for _, e := range entries {
		if err := writeFile(tw, e.disk, e.name); err != nil {
			return err
		}
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
		ModTime: time.Now().UTC(),
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
