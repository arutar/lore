package directory

import "testing"

func TestObjectGUIDFilterRoundTrip(t *testing.T) {
	// The bytes AD stores for objectGUID 4e2f9c0b-2a1d-3b4c-9e8f-7a6b5c4d3e2f.
	raw := []byte{0x0b, 0x9c, 0x2f, 0x4e, 0x1d, 0x2a, 0x4c, 0x3b, 0x9e, 0x8f, 0x7a, 0x6b, 0x5c, 0x4d, 0x3e, 0x2f}
	id, err := ObjectGUIDString(raw)
	if err != nil || id != "4e2f9c0b-2a1d-3b4c-9e8f-7a6b5c4d3e2f" {
		t.Fatalf("ObjectGUIDString = %q, %v", id, err)
	}
	filter, err := objectGUIDFilter(id)
	if err != nil {
		t.Fatal(err)
	}
	want := `(objectGUID=\0b\9c\2f\4e\1d\2a\4c\3b\9e\8f\7a\6b\5c\4d\3e\2f)`
	if filter != want {
		t.Fatalf("filter = %s, want %s", filter, want)
	}
	if _, err := objectGUIDFilter("*)(objectClass=*"); err == nil {
		t.Fatal("a non-GUID value reached the LDAP filter")
	}
}
