package message2mail

import (
	"testing"
)

func TestDeliveryMarkerCanBeReadFromFetchedHeader(t *testing.T) {
	const revision = "0123456789abcdef"
	if !headerContainsRevision([]byte("X-StayInformed-Revision: "+revision+"\r\n\r\n"), revision) {
		t.Fatal("expected delivery marker to match")
	}
	if headerContainsRevision([]byte("X-StayInformed-Revision: other\r\n\r\n"), revision) {
		t.Fatal("unexpected delivery marker match")
	}
}
