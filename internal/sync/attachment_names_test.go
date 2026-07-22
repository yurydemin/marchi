package sync

import (
	"reflect"
	"testing"

	"github.com/yurydemin/marchi/internal/mimeparse"
)

func TestAttachmentNames(t *testing.T) {
	if got := attachmentNames(nil); got != nil {
		t.Errorf("attachmentNames(nil) = %v, want nil", got)
	}
	if got := attachmentNames([]mimeparse.Attachment{}); got != nil {
		t.Errorf("attachmentNames(empty) = %v, want nil", got)
	}

	got := attachmentNames([]mimeparse.Attachment{
		{Filename: "report.pdf"},
		{Filename: "logo.png"},
	})
	want := []string{"report.pdf", "logo.png"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("attachmentNames = %v, want %v", got, want)
	}
}
