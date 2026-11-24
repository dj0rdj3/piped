package execution

import (
	"bytes"
	"strings"
	"testing"
)

func TestControlRecordAcrossEveryPacketBoundary(t *testing.T) {
	payload := "before\n\x1epiped-fixture:12345\x1fafter\n"
	for boundary := 0; boundary <= len(payload); boundary++ {
		var output bytes.Buffer
		group := make(chan int, 1)
		writer := &controlOutput{output: &output, marker: "\x1epiped-fixture:", group: group}
		if _, err := writer.Write([]byte(payload[:boundary])); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(payload[boundary:])); err != nil {
			t.Fatal(err)
		}
		if err := writer.Flush(); err != nil {
			t.Fatal(err)
		}
		if output.String() != "before\nafter\n" {
			t.Fatalf("boundary=%d output=%q", boundary, output.String())
		}
		select {
		case id := <-group:
			if id != 12345 {
				t.Fatal(id)
			}
		default:
			t.Fatal("process group missing")
		}
	}
}

func TestMissingAndInvalidControlRecords(t *testing.T) {
	var output bytes.Buffer
	writer := &controlOutput{output: &output, marker: "\x1epiped-fixture:", group: make(chan int, 1)}
	message := "sudo: elevation denied\n"
	if _, err := writer.Write([]byte(message)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Flush(); err != nil {
		t.Fatal(err)
	}
	if output.String() != message {
		t.Fatal("lost terminal error before supervisor startup")
	}
	for _, value := range []string{"0", "1", "-1", "bad", strings.Repeat("9", 30)} {
		writer := &controlOutput{output: &bytes.Buffer{}, marker: "\x1epiped-fixture:", group: make(chan int, 1)}
		if _, err := writer.Write([]byte("\x1epiped-fixture:" + value + "\x1f")); err == nil {
			t.Fatalf("accepted process group %s", value)
		}
	}
}

func TestControlOutputKeepsOrdinaryPromptsLive(t *testing.T) {
	var output bytes.Buffer
	writer := &controlOutput{output: &output, marker: "\x1epiped-fixture:", group: make(chan int, 1)}
	for _, prompt := range []string{"Password: ", "\u65e5\u672c: ", "\x1enot-a-record"} {
		if _, err := writer.Write([]byte(prompt)); err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(output.String(), prompt) || writer.pending != "" {
			t.Fatal("ordinary startup output was withheld")
		}
	}
	if _, err := writer.Write([]byte("prompt\x1epip")); err != nil {
		t.Fatal(err)
	}
	if writer.pending != "\x1epip" || !strings.HasSuffix(output.String(), "prompt") {
		t.Fatal("did not retain only the possible protocol prefix")
	}
	if _, err := writer.Write([]byte("ed-fixture:123\x1fready\n")); err != nil {
		t.Fatal(err)
	}
	if <-writer.group != 123 || !strings.HasSuffix(output.String(), "promptready\n") {
		t.Fatal("split process group record was lost")
	}
}

func TestEnvironmentHandshakeAcrossEveryPacketBoundary(t *testing.T) {
	const marker = "\x1epiped-fixture:"
	payload := "before" + marker + "ready\x1fbetween" + marker + "12345\x1fafter"
	for first := 0; first <= len(payload); first++ {
		for second := first; second <= len(payload); second++ {
			var output bytes.Buffer
			writer := &controlOutput{output: &output, marker: marker, group: make(chan int, 1), ready: make(chan struct{}, 1), started: make(chan struct{})}
			for _, chunk := range []string{payload[:first], payload[first:second], payload[second:]} {
				if _, err := writer.Write([]byte(chunk)); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Flush(); err != nil {
				t.Fatal(err)
			}
			if output.String() != "beforebetweenafter" || len(writer.ready) != 1 || len(writer.group) != 1 {
				t.Fatalf("handshake changed at boundaries %d, %d", first, second)
			}
			select {
			case <-writer.started:
			default:
				t.Fatal("process startup was not announced")
			}
		}
	}
}

func TestControlOutputRejectsRepeatedReadinessAndOverlongFrames(t *testing.T) {
	const marker = "\x1epiped-fixture:"
	for _, payload := range []string{
		marker + "ready\x1f" + marker + "ready\x1f",
		marker + strings.Repeat("9", 21),
	} {
		writer := &controlOutput{output: &bytes.Buffer{}, marker: marker, group: make(chan int, 1), ready: make(chan struct{}, 1)}
		if _, err := writer.Write([]byte(payload)); err == nil {
			t.Fatal("invalid supervisor handshake was accepted")
		}
	}
}
