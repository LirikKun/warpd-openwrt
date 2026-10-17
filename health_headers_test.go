package main

import (
	"bufio"
	"fmt"
	"strings"
	"testing"
)

func TestHealthBoundsStatusAndCumulativeHeaders(t *testing.T) {
	const secret = "private-response-marker"
	for name, header := range map[string]string{
		"many short headers": "HTTP/1.1 200 OK\r\n" + strings.Repeat("X-Short: value\r\n", 2000) + "\r\n",
		"long status":        "HTTP/1.1 200 " + strings.Repeat(secret, 1000) + "\r\n\r\n",
		"long header":        "HTTP/1.1 200 OK\r\nX-Long: " + strings.Repeat(secret, 1000) + "\r\n\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			r := &healthByteReader{data: header + healthMetaJSON}
			_, err := readMetaResponse(bufio.NewReader(r))
			if err == nil {
				t.Fatal("accepted status and headers exceeding 16 KiB")
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("header error leaked peer data: %v", err)
			}
			if consumed := len(header) + len(healthMetaJSON) - len(r.data); consumed > 16*1024 {
				t.Fatalf("read beyond header budget: %d bytes", consumed)
			}
		})
	}
}

func TestHealthHeaderBudgetBoundary(t *testing.T) {
	for _, chunked := range []bool{false, true} {
		t.Run(fmt.Sprint(chunked), func(t *testing.T) {
			header := "HTTP/1.1 200 OK\r\n"
			body := healthMetaJSON
			if chunked {
				header += "Transfer-Encoding: chunked\r\n"
				body = fmt.Sprintf("%x\r\n%s\r\n0\r\n\r\n", len(body), body)
			}
			header += "X-Padding: " + strings.Repeat("x", 16*1024-len(header)-len("X-Padding: \r\n\r\n")) + "\r\n\r\n"
			if got, err := readMetaResponse(bufio.NewReader(strings.NewReader(header + body))); err != nil || got.node != "AMS" {
				t.Fatalf("valid exact-budget response rejected: node=%q err=%v", got.node, err)
			}
		})
	}
}
