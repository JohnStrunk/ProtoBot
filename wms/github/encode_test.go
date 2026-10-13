package github

import (
	"testing"
)

// TestDecodeBodyPayloadWithLiteralEndMarker covers decodeBody matching the
// end marker with LastIndex. json.Marshal escapes angle brackets, so bodies
// written by encodeBody can never contain the marker literally; a body
// written out of band with unescaped JSON can. The first-occurrence match
// used to truncate the payload at the embedded marker and make the whole
// record undecodable, which the fail-closed scan then reports as
// WMS_UNAVAILABLE instead of serving the record.
func TestDecodeBodyPayloadWithLiteralEndMarker(t *testing.T) {
	body := bodyBegin + "\n" +
		`{"kind":"request","project_id":"fixture-project","request":{"request_id":"REQ-00001","intent":"x <!-- /protobot-wms-v1 --> y"}}` +
		"\n" + bodyEnd + "\n"

	doc, err := decodeBody(body)
	if err != nil {
		t.Fatalf("decodeBody with a literal end marker inside the payload: %v", err)
	}
	if doc.Kind != kindRequest || doc.ProjectID != "fixture-project" || doc.Request == nil {
		t.Fatalf("decoded document = %#v", doc)
	}
	if doc.Request.ID != "REQ-00001" || doc.Request.Intent != "x <!-- /protobot-wms-v1 --> y" {
		t.Fatalf("decoded request = %#v, want the embedded marker preserved verbatim", doc.Request)
	}
}
