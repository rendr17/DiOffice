package runtimeevents

import (
	"bufio"
	"context"
	"io"
	"strings"
)

const sseMaxLine = 512 * 1024

// readSSEData yields the joined `data:` payload of each SSE frame. Comments,
// event names, and ids are ignored — the provider sends JSON `data:` lines.
func readSSEData(ctx context.Context, body io.Reader, emit func([]byte) error) error {
	reader := bufio.NewReaderSize(body, 64*1024)
	var data strings.Builder
	flush := func() error {
		if data.Len() == 0 {
			return nil
		}
		payload := data.String()
		data.Reset()
		return emit([]byte(strings.TrimRight(payload, "\n")))
	}
	for {
		line, err := reader.ReadString('\n')
		if len(line) > sseMaxLine {
			return io.ErrUnexpectedEOF
		}
		trimmed := strings.TrimRight(line, "\r\n")
		switch {
		case trimmed == "":
			if ferr := flush(); ferr != nil {
				return ferr
			}
		case strings.HasPrefix(trimmed, ":"):
			// heartbeat comment
		case strings.HasPrefix(trimmed, "data:"):
			data.WriteString(strings.TrimPrefix(trimmed, "data:"))
			data.WriteString("\n")
		default:
			// event:, id:, retry:, unknown fields — ignored
		}
		if err != nil {
			if err == io.EOF {
				_ = flush()
			}
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
}
