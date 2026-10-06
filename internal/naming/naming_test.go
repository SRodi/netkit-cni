package naming

import "testing"

func TestHostInterface(t *testing.T) {
	a, err := HostInterface("container-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := HostInterface("container-a")
	if err != nil {
		t.Fatal(err)
	}
	c, err := HostInterface("container-b")
	if err != nil {
		t.Fatal(err)
	}
	if a != b || a == c {
		t.Fatalf("names are not deterministic and distinct: %q %q %q", a, b, c)
	}
	if len(a) > LinuxInterfaceNameMax {
		t.Fatalf("name %q is longer than %d bytes", a, LinuxInterfaceNameMax)
	}
	if _, err := HostInterface(""); err == nil {
		t.Fatal("empty container ID succeeded")
	}
}
