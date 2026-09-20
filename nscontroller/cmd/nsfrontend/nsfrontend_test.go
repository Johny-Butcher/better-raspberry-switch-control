package main

// This file contains unit tests for nsfrontend dispatcher selection and flag parsing.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/omakoto/raspberry-switch-control/nscontroller/js"
)

func TestOpenOutputUsesStdoutByDefault(t *testing.T) {
	if *out != "" {
		t.Fatalf("unexpected frontend output default: %q", *out)
	}
	output, err := openOutput("")
	if err != nil {
		t.Fatal(err)
	}
	if output != os.Stdout {
		t.Fatal("empty output path should use stdout")
	}
}

func TestOpenOutputAppendsToConfiguredPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backend.fifo")
	if err := os.WriteFile(path, []byte("existing\n"), 0666); err != nil {
		t.Fatal(err)
	}
	output, err := openOutput(path)
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	if _, err := output.Write([]byte("next\n")); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "existing\nnext\n" {
		t.Fatalf("output was not appended: %q", content)
	}
}

func TestMustGetDispatcher(t *testing.T) {
	tests := []struct {
		name       string
		deviceName string
	}{
		{"XboxOne", "Microsoft X-Box One pad"},
		{"Xbox360", "Xbox 360 Wireless Receiver"},
		{"NintendoSwitchPro", "Nintendo Switch Pro Controller"},
		{"PS4", "Sony Interactive Entertainment Wireless Controller"},
		{"PS4_Old", "Sony Computer Entertainment Wireless Controller"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dummyJs := &js.Js{Name: tc.deviceName}
			dispatcher := mustGetDispatcher(dummyJs)
			if dispatcher == nil {
				t.Fatalf("expected non-nil dispatcher for device %q", tc.deviceName)
			}
		})
	}
}
