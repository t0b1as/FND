package api

// Bildausrichtung aus den EXIF-Daten (R632).
//
// Handys speichern ein hochkant aufgenommenes Foto meist LIEGEND und vermerken
// die nötige Drehung nur als EXIF-Angabe (Tag 0x0112, "Orientation"). Browser
// beachten das beim Anzeigen, die Verkleinerung im Node bisher nicht – deshalb
// lagen hochkant aufgenommene Vorschaubilder um 90 Grad gedreht.
//
// Gelesen wird nur diese eine Angabe, mit der Standardbibliothek: APP1-Segment
// suchen, TIFF-Kopf prüfen, erste IFD durchgehen. Keine fremde Bibliothek.

import (
	"encoding/binary"
	"image"
	"image/color"
)

// exifOrientation liefert 1..8 nach EXIF-Standard; 1 bedeutet "unverändert".
// Bei fehlenden oder unlesbaren Daten ebenfalls 1.
func exifOrientation(data []byte) int {
	// JPEG? Muss mit FFD8 beginnen.
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return 1
	}
	i := 2
	for i+4 <= len(data) {
		if data[i] != 0xFF {
			i++ // Füllbytes überspringen
			continue
		}
		marker := data[i+1]
		if marker == 0xD8 || marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7) {
			i += 2
			continue
		}
		if marker == 0xDA || marker == 0xD9 {
			return 1 // ab hier kommen Bilddaten
		}
		if i+4 > len(data) {
			return 1
		}
		segLen := int(binary.BigEndian.Uint16(data[i+2 : i+4]))
		if segLen < 2 || i+2+segLen > len(data) {
			return 1
		}
		if marker == 0xE1 { // APP1 – hier steckt EXIF
			seg := data[i+4 : i+2+segLen]
			if o := orientationFromExif(seg); o != 0 {
				return o
			}
		}
		i += 2 + segLen
	}
	return 1
}

// orientationFromExif liest die Angabe aus einem APP1-Segment. 0 = nicht gefunden.
func orientationFromExif(seg []byte) int {
	if len(seg) < 14 || string(seg[:4]) != "Exif" {
		return 0
	}
	tiff := seg[6:] // "Exif\0\0" überspringen
	if len(tiff) < 8 {
		return 0
	}
	var bo binary.ByteOrder
	switch {
	case tiff[0] == 'I' && tiff[1] == 'I':
		bo = binary.LittleEndian
	case tiff[0] == 'M' && tiff[1] == 'M':
		bo = binary.BigEndian
	default:
		return 0
	}
	if bo.Uint16(tiff[2:4]) != 42 {
		return 0
	}
	off := int(bo.Uint32(tiff[4:8]))
	if off < 8 || off+2 > len(tiff) {
		return 0
	}
	anzahl := int(bo.Uint16(tiff[off : off+2]))
	pos := off + 2
	for n := 0; n < anzahl && pos+12 <= len(tiff); n++ {
		tag := bo.Uint16(tiff[pos : pos+2])
		if tag == 0x0112 { // Orientation
			typ := bo.Uint16(tiff[pos+2 : pos+4])
			if typ == 3 { // SHORT: Wert steht direkt im Eintrag
				v := int(bo.Uint16(tiff[pos+8 : pos+10]))
				if v >= 1 && v <= 8 {
					return v
				}
			}
			return 0
		}
		pos += 12
	}
	return 0
}

// applyOrientation dreht bzw. spiegelt das Bild gemäß EXIF-Angabe.
// Wird auf das bereits VERKLEINERTE Bild angewendet – dort kostet es nichts.
func applyOrientation(src *image.RGBA, o int) *image.RGBA {
	if src == nil || o <= 1 || o > 8 {
		return src
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	// 5..8 vertauschen Breite und Höhe.
	nw, nh := w, h
	if o >= 5 {
		nw, nh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var nx, ny int
			switch o {
			case 2: // waagerecht gespiegelt
				nx, ny = w-1-x, y
			case 3: // 180 Grad
				nx, ny = w-1-x, h-1-y
			case 4: // senkrecht gespiegelt
				nx, ny = x, h-1-y
			case 5: // an der Hauptdiagonale gespiegelt
				nx, ny = y, x
			case 6: // 90 Grad im Uhrzeigersinn
				nx, ny = h-1-y, x
			case 7: // an der Nebendiagonale gespiegelt
				nx, ny = h-1-y, w-1-x
			case 8: // 90 Grad gegen den Uhrzeigersinn
				nx, ny = y, w-1-x
			}
			i := src.PixOffset(b.Min.X+x, b.Min.Y+y)
			dst.SetRGBA(nx, ny, color.RGBA{
				R: src.Pix[i], G: src.Pix[i+1], B: src.Pix[i+2], A: 255,
			})
		}
	}
	return dst
}
