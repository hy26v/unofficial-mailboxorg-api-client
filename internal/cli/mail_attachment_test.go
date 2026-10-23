// ABOUTME: CLI tests for "mail attachment" without --save-to: text extraction from PDF, DOCX,
// ABOUTME: HTML and text files, paging long text with offsets, base64 output and its size bound.
package cli

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

// testPDF builds a two-page PDF: page 1 has two lines of text, page 2 only an image.
func testPDF() []byte {
	content := "BT /F1 12 Tf 72 720 Td (Hello Project Atlas) Tj 0 -20 Td (Delivery in 30 days) Tj ET"
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R 6 0 R] /Count 2 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /XObject << /Im1 7 0 R >> >> /Contents 8 0 R >>",
		"<< /Type /XObject /Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 8 /Length 1 >>\nstream\n\x00\nendstream",
		"<< /Length 31 >>\nstream\nq 100 0 0 100 0 0 cm /Im1 Do Q\nendstream",
	}
	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects))
	for i, object := range objects {
		offsets[i] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return out.Bytes()
}

// testDOCX builds a DOCX file with two paragraphs, a table row and an image.
func testDOCX(t *testing.T) []byte {
	t.Helper()
	var out bytes.Buffer
	archive := zip.NewWriter(&out)
	for name, content := range map[string]string{
		"word/document.xml": `<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` +
			`<w:p><w:r><w:t>Contrato</w:t></w:r><w:r><w:t xml:space="preserve"> Project Atlas</w:t></w:r></w:p>` +
			`<w:p><w:r><w:t>Plazo:</w:t><w:tab/><w:t>30 días</w:t></w:r></w:p>` +
			`<w:tbl><w:tr><w:tc><w:p><w:r><w:t>A</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>B</w:t></w:r></w:p></w:tc></w:tr></w:tbl>` +
			`</w:body></w:document>`,
		"word/media/image1.png": "\x89PNG",
	} {
		writer, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		writer.Write([]byte(content))
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func newMailAttachmentServer(t *testing.T, part map[string]any, content []byte) *testutil.Server {
	t.Helper()
	srv := newMailReadServer(t, mailReadMessage(plainBodyPart, part))
	srv.Handle("/ajax/mail", "attachment", testutil.Response{Body: content})
	return srv
}

func runMailAttachmentContent(t *testing.T, srv *testutil.Server, extra ...string) mailAttachmentContent {
	t.Helper()
	args := append([]string{"mail", "attachment", "--id", "7", "--folder", "default0/INBOX", "--attachment", "2", "--llm"}, extra...)
	stdout, stderr, code := run(t, srv, mailReadEnv, args)
	if code != 0 {
		t.Fatalf("%v: exit %d; stderr=%s", extra, code, stderr)
	}
	var result mailAttachmentContent
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	return result
}

func TestMailAttachment_PDFTextReportsPagesWithoutTextAndImages(t *testing.T) {
	srv := newMailAttachmentServer(t, pdfPart, testPDF())
	defer srv.Close()

	result := runMailAttachmentContent(t, srv)
	if result.Format != "text" || result.Text == nil || *result.Text != "Hello Project Atlas\nDelivery in 30 days" {
		t.Errorf("text = %v, want the two lines of page 1", result.Text)
	}
	if result.PageCount == nil || *result.PageCount != 2 || fmt.Sprint(result.PagesWithoutText) != "[2]" || !result.HasImages {
		t.Errorf("result = %+v, want 2 pages, page 2 without text, images", result)
	}
	if notes := strings.Join(result.Notes, " | "); !strings.Contains(notes, "pages 2 have no text layer") || !strings.Contains(notes, "contains images") {
		t.Errorf("notes = %q, want the scanned page and the images named", notes)
	}
	if result.Truncated || result.NextOffset != nil || result.Bytes != len(*result.Text) || result.TotalBytes != result.Bytes || result.Filename != "invoice.pdf" {
		t.Errorf("result = %+v, want the complete text", result)
	}
	for _, req := range srv.Requests() {
		if req.URL.Query().Get("action") == "get" && req.URL.Query().Get("unseen") != "true" {
			t.Errorf("the lookup marked the message seen: %s", req.URL)
		}
	}
}

func TestMailAttachment_DOCXByExtensionKeepsParagraphsAndTabs(t *testing.T) {
	part := map[string]any{"id": "2", "disp": "attachment", "content_type": "application/octet-stream", "filename": "Contrato.docx", "size": 900, "content": nil}
	srv := newMailAttachmentServer(t, part, testDOCX(t))
	defer srv.Close()

	result := runMailAttachmentContent(t, srv)
	if result.Text == nil || *result.Text != "Contrato Project Atlas\nPlazo:\t30 días\nA\n\tB" || !result.HasImages || result.PageCount != nil {
		t.Errorf("result = %+v, text %q", result, *result.Text)
	}
}

func TestMailAttachment_LongTextPagesWithOffset(t *testing.T) {
	part := map[string]any{"id": "2", "disp": "attachment", "content_type": "text/csv", "filename": "rows.csv", "size": 3000, "content": nil}
	text := strings.Repeat("ñandú,", 500) // 3500 bytes, two-byte characters
	srv := newMailAttachmentServer(t, part, []byte(text))
	defer srv.Close()

	var pieces []string
	offset := 0
	for page := 0; page < 10; page++ {
		result := runMailAttachmentContent(t, srv, "--max-bytes", "1024", "--offset", fmt.Sprint(offset))
		pieces = append(pieces, *result.Text)
		if result.Offset != offset || result.TotalBytes != len(text) || result.Bytes > 1024 {
			t.Errorf("page %d = offset %d bytes %d total %d", page, result.Offset, result.Bytes, result.TotalBytes)
		}
		if !result.Truncated {
			break
		}
		offset = *result.NextOffset
	}
	if strings.Join(pieces, "") != text || len(pieces) != 4 {
		t.Errorf("pieces = %d, joined text matches: %v", len(pieces), strings.Join(pieces, "") == text)
	}
	// An offset inside a character starts at that character.
	if result := runMailAttachmentContent(t, srv, "--offset", "1"); result.Offset != 0 {
		t.Errorf("offset 1 started at %d, want 0", result.Offset)
	}
}

func TestMailAttachment_HTMLAndLatin1Text(t *testing.T) {
	htmlPart := map[string]any{"id": "2", "disp": "attachment", "content_type": "text/html", "filename": "page.html", "size": 90, "content": nil}
	srv := newMailAttachmentServer(t, htmlPart, []byte(`<p>Pedido <a href="https://example.org/o?utm_source=x">aquí</a></p><p>Fin &amp; más</p>`))
	defer srv.Close()
	if result := runMailAttachmentContent(t, srv); *result.Text != "Pedido aquí (https://example.org/o)\nFin & más" {
		t.Errorf("html text = %q", *result.Text)
	}
	if got := decodeTextBytes([]byte("Espa\xf1a")); got != "España" {
		t.Errorf("latin-1 text = %q", got)
	}
	if got := decodeTextBytes([]byte{0xFF, 0xFE, 'O', 0, 'K', 0}); got != "OK" {
		t.Errorf("UTF-16 text = %q", got)
	}
}

func TestMailAttachment_Base64AndUnsupportedTypes(t *testing.T) {
	image := map[string]any{"id": "2", "disp": "attachment", "content_type": "image/png", "filename": "photo.png", "size": 4, "content": nil}
	srv := newMailAttachmentServer(t, image, []byte("\x89PNG"))
	defer srv.Close()

	result := runMailAttachmentContent(t, srv)
	if *result.Text != "" || !result.HasImages || len(result.Notes) != 1 || !strings.Contains(result.Notes[0], "content base64") {
		t.Errorf("image as text = %+v, want an empty text and a note", result)
	}
	result = runMailAttachmentContent(t, srv, "--content", "base64")
	if result.ContentBase64 == nil || *result.ContentBase64 != base64.StdEncoding.EncodeToString([]byte("\x89PNG")) || result.Text != nil || result.Bytes != 4 {
		t.Errorf("base64 = %+v", result)
	}

	big := newMailAttachmentServer(t, image, bytes.Repeat([]byte{1}, 2000))
	defer big.Close()
	_, stderr, code := run(t, big, mailReadEnv, []string{"mail", "attachment", "--id", "7", "--folder", "default0/INBOX", "--attachment", "2", "--content", "base64", "--max-bytes", "1024", "--llm"})
	if code == 0 || !strings.Contains(stderr, "output_too_large") || !strings.Contains(stderr, "2000 bytes") {
		t.Errorf("base64 over the cap: code %d, stderr = %s", code, stderr)
	}

	zipPart := map[string]any{"id": "2", "disp": "attachment", "content_type": "application/zip", "filename": "files.zip", "size": 4, "content": nil}
	other := newMailAttachmentServer(t, zipPart, []byte("PK\x03\x04"))
	defer other.Close()
	_, stderr, code = run(t, other, mailReadEnv, []string{"mail", "attachment", "--id", "7", "--folder", "default0/INBOX", "--attachment", "2", "--llm"})
	if code == 0 || !strings.Contains(stderr, "unsupported_content") || !strings.Contains(stderr, "content base64") {
		t.Errorf("zip as text: code %d, stderr = %s", code, stderr)
	}
	broken := newMailAttachmentServer(t, pdfPart, []byte("%PDF-1.4 not really"))
	defer broken.Close()
	_, stderr, code = run(t, broken, mailReadEnv, []string{"mail", "attachment", "--id", "7", "--folder", "default0/INBOX", "--attachment", "2", "--llm"})
	if code == 0 || !strings.Contains(stderr, "unsupported_content") {
		t.Errorf("broken PDF: code %d, stderr = %s", code, stderr)
	}
}

func TestMailAttachment_HumanOutputShowsTheText(t *testing.T) {
	srv := newMailAttachmentServer(t, pdfPart, testPDF())
	defer srv.Close()
	stdout, stderr, code := run(t, srv, mailReadEnv, []string{"mail", "attachment", "--id", "7", "--folder", "default0/INBOX", "--attachment", "2"})
	if code != 0 || !strings.Contains(stdout, "page_count") || !strings.Contains(stdout, "Delivery in 30 days") {
		t.Errorf("code %d, stdout = %s, stderr = %s", code, stdout, stderr)
	}
}

// testXLSX builds an XLSX file with a shared string, an inline string, a number and a
// gap between cells, in two sheets.
func testXLSX(t *testing.T) []byte {
	t.Helper()
	var out bytes.Buffer
	archive := zip.NewWriter(&out)
	main := `xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"`
	for name, content := range map[string]string{
		"xl/workbook.xml": `<workbook ` + main + `><sheets><sheet name="Socios" sheetId="1" r:id="rId1"/><sheet name="Notas" sheetId="2" r:id="rId2"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId1" Target="worksheets/sheet1.xml"/><Relationship Id="rId2" Target="/xl/worksheets/other.xml"/></Relationships>`,
		"xl/sharedStrings.xml": `<sst ` + main + `><si><t>Nombre</t></si><si><r><t>Im</t></r><r><t>porte</t></r></si><si><t>Ana</t></si></sst>`,
		"xl/worksheets/sheet1.xml": `<worksheet ` + main + `><sheetData>` +
			`<row r="1"><c r="A1" t="s"><v>0</v></c><c r="C1" t="s"><v>1</v></c></row>` +
			`<row r="2"><c r="A2" t="s"><v>2</v></c><c r="B2" t="b"><v>1</v></c><c r="C2"><v>1234.5</v></c></row></sheetData></worksheet>`,
		"xl/worksheets/other.xml": `<worksheet ` + main + `><sheetData><row r="1"><c r="B1" t="inlineStr"><is><t>pendiente</t></is></c></row></sheetData></worksheet>`,
	} {
		writer, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		writer.Write([]byte(content))
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestMailAttachment_XLSXSheetsAsTabSeparatedRows(t *testing.T) {
	part := map[string]any{"id": "2", "disp": "attachment", "content_type": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "filename": "Cuenta.xlsx", "size": 900, "content": nil}
	srv := newMailAttachmentServer(t, part, testXLSX(t))
	defer srv.Close()
	result := runMailAttachmentContent(t, srv)
	if want := "Sheet: Socios\nNombre\t\tImporte\nAna\tTRUE\t1234.5\n\nSheet: Notas\n\tpendiente"; result.Text == nil || *result.Text != want {
		t.Errorf("text = %q, want %q", *result.Text, want)
	}
	if len(result.Notes) != 1 || !strings.Contains(result.Notes[0], "serial numbers") {
		t.Errorf("notes = %v", result.Notes)
	}
}

func TestMailAttachment_PDFWithDataAfterTheEnd(t *testing.T) {
	data := append(testPDF(), []byte("<script>alert('x');window.close();</script>")...)
	srv := newMailAttachmentServer(t, pdfPart, data)
	defer srv.Close()
	result := runMailAttachmentContent(t, srv)
	if result.Text == nil || !strings.Contains(*result.Text, "Hello Project Atlas") || !strings.Contains(strings.Join(result.Notes, " "), "after the end of the PDF") {
		t.Errorf("result = %+v", result)
	}
}
