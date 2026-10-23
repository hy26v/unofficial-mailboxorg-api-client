// ABOUTME: Text extraction for mail attachments: PDF, DOCX, XLSX, ODT, HTML and text files, in memory.
// ABOUTME: Reports pages without text and images, so a caller knows what the text leaves out.
package cli

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"

	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
)

const (
	// attachmentTextMaxBytes bounds the text extracted from one attachment.
	attachmentTextMaxBytes = 16 << 20
	// attachmentArchiveMaxBytes bounds the unpacked XML read from a DOCX or ODT file.
	attachmentArchiveMaxBytes = 64 << 20
	// attachmentPDFMaxPages bounds the pages whose text is read.
	attachmentPDFMaxPages = 2000
)

// attachmentText is the text of one attachment and what the text does not show.
type attachmentText struct {
	text             string
	pageCount        *int
	pagesWithoutText []int
	hasImages        bool
	notes            []string
}

// attachmentKind names the extractor for a content type or, for a generic type such as
// application/octet-stream, for the file name extension.
func attachmentKind(contentType, filename string) string {
	extension := strings.ToLower(path.Ext(filename))
	switch {
	case contentType == "application/pdf" || extension == ".pdf":
		return "pdf"
	case contentType == "application/vnd.openxmlformats-officedocument.wordprocessingml.document" || extension == ".docx":
		return "docx"
	case contentType == "application/vnd.oasis.opendocument.text" || extension == ".odt":
		return "odt"
	case contentType == "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" || extension == ".xlsx":
		return "xlsx"
	case contentType == "text/html" || contentType == "application/xhtml+xml" || extension == ".html" || extension == ".htm":
		return "html"
	case strings.HasPrefix(contentType, "image/"):
		return "image"
	case strings.HasPrefix(contentType, "text/") || contentType == "application/json" || contentType == "application/xml" ||
		contentType == "message/delivery-status" || contentType == "application/pgp-signature":
		return "text"
	}
	switch extension {
	case ".txt", ".csv", ".tsv", ".md", ".ics", ".vcf", ".json", ".xml", ".log", ".eml":
		return "text"
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".heic", ".tif", ".tiff", ".bmp":
		return "image"
	}
	return ""
}

// extractAttachmentText returns the text of an attachment. A type without text, such as an
// image, gives empty text with a note; an unknown type is an unsupported_content error.
func extractAttachmentText(data []byte, contentType, filename string) (attachmentText, *cliErrors.Error) {
	var result attachmentText
	var err error
	switch kind := attachmentKind(contentType, filename); kind {
	case "pdf":
		result, err = pdfText(data)
	case "docx":
		result, err = officeText(data, "word/document.xml", "word/media/", docxTextElements)
	case "odt":
		result, err = officeText(data, "content.xml", "Pictures/", odtTextElements)
	case "xlsx":
		result, err = xlsxText(data)
	case "html":
		result.text = cleanTextURLs(htmlToTextWithLinks(decodeTextBytes(data), mailLinksClean))
	case "text":
		result.text = strings.ReplaceAll(decodeTextBytes(data), "\r\n", "\n")
	case "image":
		result.hasImages = true
		result.notes = append(result.notes, "the attachment is an image, which has no text; content base64 returns the file")
	default:
		return attachmentText{}, &cliErrors.Error{Code: "unsupported_content", Message: fmt.Sprintf("no text extraction for %s (%s); content base64 returns the file, or the mbx CLI saves it with --save-to", contentType, filename)}
	}
	if err != nil {
		return attachmentText{}, &cliErrors.Error{Code: "unsupported_content", Message: fmt.Sprintf("could not read the text of %s: %v; content base64 returns the file", filename, err)}
	}
	if len(result.text) > attachmentTextMaxBytes {
		result.text = truncateUTF8(result.text, attachmentTextMaxBytes)
		result.notes = append(result.notes, fmt.Sprintf("the text was cut at %d bytes", attachmentTextMaxBytes))
	}
	if strings.TrimSpace(result.text) == "" && len(result.notes) == 0 {
		result.notes = append(result.notes, "the attachment has no text")
	}
	return result, nil
}

// decodeTextBytes decodes a text file: UTF-16 with a byte order mark, UTF-8, or else
// ISO-8859-1, which every byte sequence is.
func decodeTextBytes(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}):
		data = data[3:]
	case bytes.HasPrefix(data, []byte{0xFF, 0xFE}), bytes.HasPrefix(data, []byte{0xFE, 0xFF}):
		units := make([]uint16, 0, len(data)/2)
		for i := 2; i+1 < len(data); i += 2 {
			if data[0] == 0xFF {
				units = append(units, uint16(data[i])|uint16(data[i+1])<<8)
			} else {
				units = append(units, uint16(data[i])<<8|uint16(data[i+1]))
			}
		}
		return string(utf16.Decode(units))
	}
	if utf8.Valid(data) {
		return string(data)
	}
	return latin1ToUTF8(data)
}

// pdfText reads the text layer of each page. Text in images, as in a scanned page, is not
// read; such pages are listed in pagesWithoutText. A malformed page is skipped with a note.
func pdfText(data []byte) (result attachmentText, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("malformed PDF: %v", recovered)
		}
	}()
	reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		// Some PDF writers and download pages append data, such as an HTML script, after
		// the final %%EOF; the pdf package then finds no %%EOF at the end.
		end := bytes.LastIndex(data, []byte("%%EOF"))
		if end < 0 || len(bytes.TrimSpace(data[end+5:])) == 0 {
			return result, err
		}
		trimmed := append(append([]byte{}, data[:end+5]...), '\n')
		if reader, err = pdf.NewReader(bytes.NewReader(trimmed), int64(len(trimmed))); err != nil {
			return result, err
		}
		result.notes = append(result.notes, fmt.Sprintf("the file has %d bytes after the end of the PDF, which were ignored", len(data)-end-5))
	}
	pages := reader.NumPage()
	result.pageCount = &pages
	var text strings.Builder
	var unreadable []int
	for number := 1; number <= pages; number++ {
		if number > attachmentPDFMaxPages {
			result.notes = append(result.notes, fmt.Sprintf("only the first %d pages were read", attachmentPDFMaxPages))
			break
		}
		if text.Len() > attachmentTextMaxBytes {
			result.notes = append(result.notes, fmt.Sprintf("pages after %d were not read: the text is over %d bytes", number-1, attachmentTextMaxBytes))
			break
		}
		page := reader.Page(number)
		if page.V.IsNull() {
			continue
		}
		if pdfPageHasImages(page.Resources(), 0) {
			result.hasImages = true
		}
		pageText, ok := pdfPageText(page)
		if !ok {
			unreadable = append(unreadable, number)
		}
		if strings.TrimSpace(pageText) == "" {
			result.pagesWithoutText = append(result.pagesWithoutText, number)
			continue
		}
		if text.Len() > 0 {
			text.WriteString("\n\n")
		}
		text.WriteString(pageText)
	}
	result.text = text.String()
	if len(unreadable) > 0 {
		result.notes = append(result.notes, "the text of pages "+joinInts(unreadable)+" could not be read completely")
	}
	if len(result.pagesWithoutText) > 0 {
		result.notes = append(result.notes, "pages "+joinInts(result.pagesWithoutText)+" have no text layer, probably scanned images; their text is not read (no OCR)")
	}
	if result.hasImages {
		result.notes = append(result.notes, "the PDF contains images; text inside images is not read")
	}
	return result, nil
}

// pdfPageHasImages reports whether resources hold an image, also inside form objects, up
// to a small depth.
func pdfPageHasImages(resources pdf.Value, depth int) bool {
	if depth > 3 || resources.IsNull() {
		return false
	}
	objects := resources.Key("XObject")
	for _, name := range objects.Keys() {
		object := objects.Key(name)
		switch object.Key("Subtype").Name() {
		case "Image":
			return true
		case "Form":
			if pdfPageHasImages(object.Key("Resources"), depth+1) {
				return true
			}
		}
	}
	return false
}

// pdfPageText is the text of one page in lines. ok is false when the page could not be
// read to its end; the text then holds what was read before.
func pdfPageText(page pdf.Page) (text string, ok bool) {
	var glyphs []pdfGlyph
	ok = true
	func() {
		defer func() {
			if recover() != nil {
				ok = false
			}
		}()
		contents := page.V.Key("Contents")
		if contents.IsNull() {
			return
		}
		state := pdfTextState{ctm: pdfIdentity, tm: pdfIdentity, tlm: pdfIdentity, scale: 1}
		interpretPDFText(contents, page.Resources(), state, 0, &glyphs)
	}()
	return pdfLines(glyphs), ok
}

func joinInts(values []int) string {
	parts := make([]string, len(values))
	for i, value := range values {
		parts[i] = strconv.Itoa(value)
	}
	return strings.Join(parts, ", ")
}

// officeTextElements say how the XML of a word-processing file becomes text: the elements
// whose character data is text, and the elements that end a line or add a tab. Names are
// local names without the namespace.
type officeTextElements struct {
	text      map[string]bool // character data inside them is text; nil means all of it
	lineEnds  map[string]bool // their end tag ends a line
	lineBreak map[string]bool // the element itself is a line break
	tabs      map[string]bool // the element itself is a tab
	cellEnds  map[string]bool // their end tag adds a tab, as between table cells
	spaces    string          // an element that stands for spaces, with the count in attribute c
}

var docxTextElements = officeTextElements{
	text:      map[string]bool{"t": true},
	lineEnds:  map[string]bool{"p": true, "tr": true},
	lineBreak: map[string]bool{"br": true, "cr": true},
	tabs:      map[string]bool{"tab": true},
	cellEnds:  map[string]bool{"tc": true},
}

var odtTextElements = officeTextElements{
	lineEnds:  map[string]bool{"p": true, "h": true, "table-row": true},
	lineBreak: map[string]bool{"line-break": true},
	tabs:      map[string]bool{"tab": true},
	cellEnds:  map[string]bool{"table-cell": true},
	spaces:    "s",
}

// officeText reads the text of a DOCX or ODT file: the XML document part, read with a
// size bound, and whether the archive holds images under mediaPrefix.
func officeText(data []byte, documentPart, mediaPrefix string, elements officeTextElements) (attachmentText, error) {
	var result attachmentText
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return result, err
	}
	var document *zip.File
	for _, file := range archive.File {
		switch {
		case file.Name == documentPart:
			document = file
		case strings.HasPrefix(file.Name, mediaPrefix) && !strings.HasSuffix(file.Name, "/"):
			result.hasImages = true
		}
	}
	if document == nil {
		return result, fmt.Errorf("the file has no %s", documentPart)
	}
	part, err := document.Open()
	if err != nil {
		return result, err
	}
	defer part.Close()
	text, err := officeXMLText(io.LimitReader(part, attachmentArchiveMaxBytes), elements)
	if err != nil {
		return result, err
	}
	result.text = text
	if result.hasImages {
		result.notes = append(result.notes, "the document contains images; text inside images is not read")
	}
	return result, nil
}

func officeXMLText(input io.Reader, elements officeTextElements) (string, error) {
	decoder := xml.NewDecoder(input)
	decoder.Strict = false
	var out strings.Builder
	depth := 0 // how many open text elements contain the current position
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		switch t := token.(type) {
		case xml.StartElement:
			name := t.Name.Local
			switch {
			case elements.text[name]:
				depth++
			case elements.lineBreak[name]:
				out.WriteByte('\n')
			case elements.tabs[name]:
				out.WriteByte('\t')
			case elements.spaces != "" && name == elements.spaces:
				count := 1
				for _, attribute := range t.Attr {
					if attribute.Name.Local == "c" {
						if n, err := strconv.Atoi(attribute.Value); err == nil && n > 0 && n < 1000 {
							count = n
						}
					}
				}
				out.WriteString(strings.Repeat(" ", count))
			}
		case xml.EndElement:
			name := t.Name.Local
			switch {
			case elements.text[name]:
				depth--
			case elements.lineEnds[name]:
				out.WriteByte('\n')
			case elements.cellEnds[name]:
				out.WriteByte('\t')
			}
		case xml.CharData:
			if elements.text == nil || depth > 0 {
				out.Write(t)
			}
		}
		if out.Len() > attachmentTextMaxBytes {
			break
		}
	}
	lines := strings.Split(out.String(), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	return strings.TrimSpace(blankLinesPattern.ReplaceAllString(strings.Join(lines, "\n"), "\n\n")), nil
}
