package sp

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// Authenticate must refuse the device-code fallback when no terminal is
// attached. Device code is a polling flow, so without this guard an unattended
// caller prints a code nobody can see and then blocks until it expires. The
// error has to arrive immediately and name the command that fixes it.
func TestAuthenticateRefusesDeviceCodeWithoutTerminal(t *testing.T) {
	restore := interactive
	interactive = func() bool { return false }
	t.Cleanup(func() { interactive = restore })

	// A client over an empty cache has no accounts, so silent acquisition is
	// skipped and control reaches the guard without any network call. A
	// zero-value public.Client cannot stand in here — MSAL dereferences its
	// internals and panics.
	client, err := NewPublicClient(filepath.Join(t.TempDir(), "sp-token.json"))
	if err != nil {
		t.Fatalf("NewPublicClient: %v", err)
	}

	_, err = Authenticate(context.Background(), client)
	if err == nil {
		t.Fatal("Authenticate succeeded with no terminal; want refusal before the device-code flow")
	}

	msg := err.Error()
	for _, want := range []string{"no terminal", "xql sp"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %q", msg, want)
		}
	}
	if strings.Contains(msg, "device code authentication") {
		t.Errorf("error %q came from the device-code flow; the guard should precede it", msg)
	}
}
