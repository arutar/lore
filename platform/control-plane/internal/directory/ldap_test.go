package directory

import (
	"encoding/base64"
	"testing"
)

func TestObjectGUIDStringUsesMicrosoftByteOrder(t *testing.T) {
	raw, err := base64.StdEncoding.DecodeString("humVWn7kO0enTqsrp37RVA==")
	if err != nil {
		t.Fatal(err)
	}
	id, err := ObjectGUIDString(raw)
	if err != nil {
		t.Fatal(err)
	}
	if id != "5a95e986-e47e-473b-a74e-ab2ba77ed154" {
		t.Fatalf("unexpected GUID: %s", id)
	}
}

func TestObjectGUIDStringRejectsInvalidInput(t *testing.T) {
	if _, err := ObjectGUIDString([]byte("not-a-guid")); err == nil {
		t.Fatal("expected invalid GUID error")
	}
}
