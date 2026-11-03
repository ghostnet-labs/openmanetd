// Command sitegen builds the embedded P25 site table (sites.dat.gz) from
// site-tuner-fit.csv and the trunked rows of scanner-frequencies.csv. It is
// run by go generate in internal/scanner/sites.
package main

import (
	"bytes"
	"compress/gzip"
	"flag"
	"fmt"
	"os"
)

func main() {
	fitPath := flag.String("fit", "", "site-tuner-fit.csv path")
	freqPath := flag.String("freq", "", "scanner-frequencies.csv path")
	outPath := flag.String("out", "sites.dat.gz", "output path")

	flag.Parse()

	if err := run(*fitPath, *freqPath, *outPath); err != nil {
		fmt.Fprintln(os.Stderr, "sitegen:", err)
		os.Exit(1)
	}
}

func run(fitPath, freqPath, outPath string) error {
	fit, err := os.Open(fitPath)
	if err != nil {
		return fmt.Errorf("open fit csv: %w", err)
	}
	defer fit.Close()

	freq, err := os.Open(freqPath)
	if err != nil {
		return fmt.Errorf("open frequency csv: %w", err)
	}
	defer freq.Close()

	var raw bytes.Buffer
	if err = build(fit, freq, &raw); err != nil {
		return err
	}

	var out bytes.Buffer

	// No name or mtime in the header so regeneration is byte-stable.
	zw, err := gzip.NewWriterLevel(&out, gzip.BestCompression)
	if err != nil {
		return fmt.Errorf("gzip writer: %w", err)
	}

	if _, err = zw.Write(raw.Bytes()); err != nil {
		return fmt.Errorf("compress: %w", err)
	}

	if err = zw.Close(); err != nil {
		return fmt.Errorf("compress: %w", err)
	}

	//nolint:gosec // generated source file, world-readable like the rest of the tree
	if err = os.WriteFile(outPath, out.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", outPath, err)
	}

	return nil
}
