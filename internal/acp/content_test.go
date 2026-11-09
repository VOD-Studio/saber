package acp

import "testing"

func TestPromptContentPreservesMediaWithoutReadingResourceURI(t *testing.T) {
	var blocks []contentBlock
	blocks = append(blocks, contentBlock{Type: "image", Data: "aGVsbG8=", MIMEType: "image/png"})
	text, attachments, err := promptContent(blocks)
	if err != nil || text != "" || len(attachments) != 1 || attachments[0].URL != "data:image/png;base64,aGVsbG8=" {
		t.Fatalf("lost image: %q %+v %+v", text, attachments, err)
	}
	_, _, err = promptContent([]contentBlock{{Type: "resource", Resource: &struct {
		URI  string `json:"uri"`
		Text string `json:"text"`
	}{URI: "file:///etc/passwd"}}})
	if err == nil {
		t.Fatal("resource URI authorized file reading")
	}
}
