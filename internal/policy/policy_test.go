package policy

import (
	"testing"
)

// TestPatchBounds checks invalid flags, JSON null semantics and global security caps.
func TestPatchBounds(t *testing.T) {
	yes := true
	huge := int64(50*1024*1024 + 1)
	zero := 0
	for _, p := range []Patch{{}, {ExpectedVersion: 1}, {ExpectedVersion: 1, Flags: map[string]*bool{"unknown": &yes}}, {ExpectedVersion: 1, Flags: map[string]*bool{"allow_images": nil}}, {ExpectedVersion: 1, MaxUploadSize: &huge}, {ExpectedVersion: 1, RetentionDays: &zero}} {
		if p.Validate() == nil {
			t.Fatal("invalid settings accepted")
		}
	}
	if (Patch{ExpectedVersion: 1, Flags: map[string]*bool{"allow_images": &yes}}).Validate() != nil {
		t.Fatal("valid flag rejected")
	}
	if (Settings{Flags: map[string]bool{"allow_images": false}}).RequireFeature("allow_images") != ErrFeatureDisabled {
		t.Fatal("disabled flag accepted")
	}
	if (Settings{}).RequireFeature("unknown") != ErrFeatureDisabled {
		t.Fatal("missing flag accepted")
	}
}
