// Package chunk splits a byte stream into content-defined chunks using FastCDC.
// Content-defined boundaries mean that inserting or removing bytes in the middle
// of a file only changes the chunks around the edit, not every chunk after it —
// which is what lets hidage re-store just the delta between snapshots.
package chunk

import (
	"io"

	fastcdc "github.com/jotfs/fastcdc-go"
)

// AverageSize is the target mean chunk size. FastCDC defaults MinSize to
// AverageSize/4 and MaxSize to AverageSize*4.
const AverageSize = 1 << 20 // 1 MiB

// Split reads r and calls fn once per content-defined chunk, in order. The
// slice handed to fn is only valid for the duration of the call (the chunker
// reuses its buffer), so fn must copy any bytes it needs to retain. Chunking is
// deterministic: the same input always yields the same boundaries.
func Split(r io.Reader, fn func(data []byte) error) error {
	chunker, err := fastcdc.NewChunker(r, fastcdc.Options{AverageSize: AverageSize})
	if err != nil {
		return err
	}
	for {
		c, err := chunker.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := fn(c.Data); err != nil {
			return err
		}
	}
}
