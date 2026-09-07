package httpx

import "testing"

func TestDefaultMessages_NeutralEnglishDefaults(t *testing.T) {
	m := DefaultMessages()

	if m.ListFailed == "" {
		t.Error("ListFailed default is empty")
	}
	if m.NotFound == "" {
		t.Error("NotFound default is empty")
	}
	if m.GetFailed == "" {
		t.Error("GetFailed default is empty")
	}
}

func TestWithMessages_OverridesOnlySetFields(t *testing.T) {
	got := DefaultMessages()
	custom := Messages{NotFound: "custom not found"}
	WithMessages(custom)(&got)

	if got.NotFound != "custom not found" {
		t.Errorf("NotFound = %q, want %q", got.NotFound, "custom not found")
	}
	// Fields left empty in the override must keep their default.
	if got.ListFailed != DefaultMessages().ListFailed {
		t.Errorf("ListFailed = %q, want default %q", got.ListFailed, DefaultMessages().ListFailed)
	}
	if got.GetFailed != DefaultMessages().GetFailed {
		t.Errorf("GetFailed = %q, want default %q", got.GetFailed, DefaultMessages().GetFailed)
	}
}
