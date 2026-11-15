package qr

import (
	"fmt"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

// GenerateSVG generates an SVG QR code for the given content.
// It uses pure Go to generate the bitmap and renders clean SVG rect elements.
func GenerateSVG(content string, size int) (string, error) {
	if size <= 0 {
		size = 256
	}
	qr, err := qrcode.New(content, qrcode.Medium)
	if err != nil {
		return "", fmt.Errorf("failed to generate qr code: %w", err)
	}

	bitmap := qr.Bitmap()
	numModules := len(bitmap)
	if numModules == 0 {
		return "", fmt.Errorf("empty qr bitmap")
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="%d" height="%d" shape-rendering="crispEdges">`+"\n", numModules, numModules, size, size))
	sb.WriteString(fmt.Sprintf(`<rect width="%d" height="%d" fill="#ffffff"/>`+"\n", numModules, numModules))

	for y := 0; y < numModules; y++ {
		row := bitmap[y]
		for x := 0; x < numModules; x++ {
			if row[x] {
				sb.WriteString(fmt.Sprintf(`<rect x="%d" y="%d" width="1" height="1" fill="#000000"/>`+"\n", x, y))
			}
		}
	}
	sb.WriteString("</svg>")
	return sb.String(), nil
}

// GenerateTerminal generates a UTF-8 / ASCII terminal QR code for the given content.
// It uses half-block characters for high-density crisp rendering in any terminal.
func GenerateTerminal(content string) (string, error) {
	q, err := qrcode.New(content, qrcode.Low)
	if err != nil {
		return "", fmt.Errorf("failed to generate terminal qr code: %w", err)
	}
	return q.ToSmallString(false), nil
}

