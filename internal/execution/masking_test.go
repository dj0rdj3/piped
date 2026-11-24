package execution

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"piped/internal/secrets"
)

func TestSecretMaskingAcrossEveryPacketBoundary(t *testing.T) {
	input := "before abcdef and abc and \u65e5\u672c\r\nsecret after"
	expected := "before [REDACTED] and [REDACTED] and [REDACTED] after"
	for split := 0; split <= len(input); split++ {
		var output bytes.Buffer
		writer, err := newSecretWriter(&output, []secrets.Variable{{Value: []byte("abc")}, {Value: []byte("abcdef")}, {Value: []byte("\u65e5\u672c\nsecret")}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(writer, input[:split]); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(writer, input[split:]); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if output.String() != expected {
			t.Fatalf("split %d: %q", split, output.String())
		}
	}
}

func TestSecretMaskingKeepsPromptsLiveAndFlushesPartialValues(t *testing.T) {
	var output bytes.Buffer
	writer, err := newSecretWriter(&output, []secrets.Variable{{Value: []byte(strings.Repeat("S", 4096))}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(writer, "Your name? "); err != nil {
		t.Fatal(err)
	}
	if output.String() != "Your name? " {
		t.Fatal("unrelated prompt withheld")
	}
	if _, err := io.WriteString(writer, "SS"); err != nil {
		t.Fatal(err)
	}
	if output.String() != "Your name? " {
		t.Fatal("secret prefix emitted prematurely")
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if output.String() != "Your name? SS" {
		t.Fatal("partial non-secret output lost")
	}
}

func TestSecretMaskingOneByteWrites(t *testing.T) {
	var output bytes.Buffer
	writer, err := newSecretWriter(&output, []secrets.Variable{{Value: []byte("same")}, {Value: []byte("same")}, {Value: []byte("same-long")}})
	if err != nil {
		t.Fatal(err)
	}
	for _, letter := range []byte("same-long same same-long") {
		if _, err := writer.Write([]byte{letter}); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if output.String() != "[REDACTED] [REDACTED] [REDACTED]" {
		t.Fatal(output.String())
	}
}
