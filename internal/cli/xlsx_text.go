// ABOUTME: Text of an XLSX workbook for mail attachments: each sheet's rows as tab-separated cells.
// ABOUTME: Reads shared strings, inline strings and stored values; no formulas or number formats.
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
)

// xlsxText reads the sheets of an XLSX file in workbook order. A cell shows its stored
// value: the text of a string, or a number as stored, so a date is a serial number.
func xlsxText(data []byte) (attachmentText, error) {
	var result attachmentText
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return result, err
	}
	files := map[string]*zip.File{}
	for _, file := range archive.File {
		files[file.Name] = file
		if strings.HasPrefix(file.Name, "xl/media/") && !strings.HasSuffix(file.Name, "/") {
			result.hasImages = true
		}
	}
	read := func(name string) ([]byte, error) {
		file := files[name]
		if file == nil {
			return nil, nil
		}
		part, err := file.Open()
		if err != nil {
			return nil, err
		}
		defer part.Close()
		return io.ReadAll(io.LimitReader(part, attachmentArchiveMaxBytes))
	}

	workbook, err := read("xl/workbook.xml")
	if err != nil || workbook == nil {
		return result, fmt.Errorf("the file has no xl/workbook.xml")
	}
	var book struct {
		Sheets []struct {
			Name string `xml:"name,attr"`
			ID   string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
		} `xml:"sheets>sheet"`
	}
	if err := xml.Unmarshal(workbook, &book); err != nil {
		return result, err
	}
	targets := map[string]string{}
	if relations, err := read("xl/_rels/workbook.xml.rels"); err == nil && relations != nil {
		var rels struct {
			Items []struct {
				ID     string `xml:"Id,attr"`
				Target string `xml:"Target,attr"`
			} `xml:"Relationship"`
		}
		if xml.Unmarshal(relations, &rels) == nil {
			for _, rel := range rels.Items {
				target := strings.TrimPrefix(rel.Target, "/")
				if !strings.HasPrefix(target, "xl/") {
					target = path.Join("xl", target)
				}
				targets[rel.ID] = target
			}
		}
	}
	var shared []string
	if table, err := read("xl/sharedStrings.xml"); err == nil && table != nil {
		shared, err = xlsxSharedStrings(table)
		if err != nil {
			return result, err
		}
	}

	var out bytes.Buffer
	for index, sheet := range book.Sheets {
		target := targets[sheet.ID]
		if target == "" {
			target = fmt.Sprintf("xl/worksheets/sheet%d.xml", index+1)
		}
		content, err := read(target)
		if err != nil || content == nil {
			result.notes = append(result.notes, "sheet "+strconv.Quote(sheet.Name)+" could not be read")
			continue
		}
		if out.Len() > 0 {
			// One blank line between sheets; a sheet's last row already ends its line.
			if !bytes.HasSuffix(out.Bytes(), []byte("\n")) {
				out.WriteByte('\n')
			}
			out.WriteByte('\n')
		}
		out.WriteString("Sheet: " + sheet.Name + "\n")
		if err := xlsxSheetText(content, shared, &out); err != nil {
			result.notes = append(result.notes, "sheet "+strconv.Quote(sheet.Name)+" could not be read completely")
		}
		if out.Len() > attachmentTextMaxBytes {
			break
		}
	}
	result.text = strings.TrimSpace(out.String())
	result.notes = append(result.notes, "cells show stored values: formulas show their last result, and dates are serial numbers")
	if result.hasImages {
		result.notes = append(result.notes, "the workbook contains images; text inside images is not read")
	}
	return result, nil
}

// xlsxSharedStrings reads the shared string table: the text of each <si>, its runs joined.
func xlsxSharedStrings(data []byte) ([]string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var shared []string
	var current strings.Builder
	inItem, inText := false, false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return shared, nil
		}
		if err != nil {
			return nil, err
		}
		switch t := token.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				inItem = true
				current.Reset()
			case "t":
				inText = inItem
			case "rPh":
				inItem = false // phonetic hints repeat the text
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "si":
				shared = append(shared, current.String())
				inItem = false
			case "t":
				inText = false
			case "rPh":
				inItem = true
			}
		case xml.CharData:
			if inText {
				current.Write(t)
			}
		}
	}
}

// xlsxSheetText writes the rows of one worksheet, a tab between cells; a missing cell
// keeps its column.
func xlsxSheetText(data []byte, shared []string, out *bytes.Buffer) error {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var row []string
	var cellType, cellRef string
	var value strings.Builder
	inValue := false
	flushRow := func() {
		for len(row) > 0 && strings.TrimSpace(row[len(row)-1]) == "" {
			row = row[:len(row)-1]
		}
		if len(row) > 0 {
			out.WriteString(strings.Join(row, "\t") + "\n")
		}
		row = row[:0]
	}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch t := token.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "row":
				row = row[:0]
			case "c":
				cellType, cellRef = "", ""
				for _, attribute := range t.Attr {
					switch attribute.Name.Local {
					case "t":
						cellType = attribute.Value
					case "r":
						cellRef = attribute.Value
					}
				}
				value.Reset()
			case "v", "t":
				inValue = true
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v", "t":
				inValue = false
			case "c":
				text := value.String()
				if cellType == "s" {
					if index, err := strconv.Atoi(strings.TrimSpace(text)); err == nil && index >= 0 && index < len(shared) {
						text = shared[index]
					}
				} else if cellType == "b" {
					text = map[string]string{"1": "TRUE", "0": "FALSE"}[strings.TrimSpace(text)]
				}
				text = strings.Join(strings.Fields(text), " ")
				if column := xlsxColumn(cellRef); column > len(row) && column-len(row) < 1000 {
					for len(row) < column {
						row = append(row, "")
					}
				}
				row = append(row, text)
			case "row":
				flushRow()
			}
		case xml.CharData:
			if inValue {
				value.Write(t)
			}
		}
		if out.Len() > attachmentTextMaxBytes {
			return nil
		}
	}
}

// xlsxColumn is the zero-based column of a cell reference such as "C7", or -1.
func xlsxColumn(ref string) int {
	column := 0
	letters := 0
	for _, r := range ref {
		if r < 'A' || r > 'Z' {
			break
		}
		column = column*26 + int(r-'A'+1)
		letters++
	}
	if letters == 0 {
		return -1
	}
	return column - 1
}
